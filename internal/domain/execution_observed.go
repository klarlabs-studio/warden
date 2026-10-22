package domain

import (
	"bufio"
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

// ContainerObservation is what a container check's own workload read about
// its bounds before the configured command ran: its cgroup v2 limits and its
// routing table. It is evidence only of what the workload saw, never of what
// was requested; a field this probe could not read stays nil (ADR 0008).
type ContainerObservation struct {
	// Cgroup is "v2" when the workload saw a cgroup v2 hierarchy, the only one
	// read. Empty means no limit was observed at all.
	Cgroup string `json:"cgroup,omitempty"`
	// MemoryBytes, SwapBytes and Pids are the cgroup maxima, or -1 for "max"
	// (unbounded). Nil means not observed.
	MemoryBytes *int64 `json:"memory_bytes,omitempty"`
	SwapBytes   *int64 `json:"swap_bytes,omitempty"`
	Pids        *int64 `json:"pids,omitempty"`
	// CPUQuota and CPUPeriod are cpu.max's two fields; a quota of -1 is "max".
	CPUQuota  *int64 `json:"cpu_quota,omitempty"`
	CPUPeriod *int64 `json:"cpu_period,omitempty"`
	// Routes counts the IPv4 routes the workload saw; nil means not observed.
	// Both address families must be observed without non-loopback routes for
	// network none. Interface lists are not evidence: kernels add fallback
	// tunnel devices to every namespace.
	Routes *int64 `json:"routes,omitempty"`
	// RoutesIPv6 counts IPv6 routes outside loopback; nil means not observed.
	RoutesIPv6 *int64 `json:"routes_ipv6,omitempty"`
}

// unbounded is how a cgroup "max" is held in an observation.
const unbounded = -1

// ParseContainerObservation reads the probe's key=value lines. Unknown keys
// are ignored; a malformed value leaves its field unobserved rather than
// guessing.
func ParseContainerObservation(raw string) ContainerObservation {
	var o ContainerObservation
	sc := bufio.NewScanner(strings.NewReader(raw))
	for sc.Scan() {
		key, value, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok {
			continue
		}
		switch key {
		case "cgroup":
			if value == "v2" {
				o.Cgroup = "v2"
			}
		case "memory.max":
			o.MemoryBytes = cgroupMax(value)
		case "memory.swap.max":
			o.SwapBytes = cgroupMax(value)
		case "pids.max":
			o.Pids = cgroupMax(value)
		case "cpu.max":
			quota, period, ok := strings.Cut(value, " ")
			if q, p := cgroupMax(quota), cgroupMax(period); ok && q != nil && p != nil && *p > 0 {
				o.CPUQuota, o.CPUPeriod = q, p
			}
		case "routes", "routes6":
			if n, err := strconv.ParseInt(value, 10, 64); err == nil && n >= 0 {
				if key == "routes" {
					o.Routes = &n
				} else {
					o.RoutesIPv6 = &n
				}
			}
		}
	}
	if o.Cgroup != "v2" {
		// Limits read outside a cgroup v2 hierarchy are not trusted.
		o.MemoryBytes, o.SwapBytes, o.Pids, o.CPUQuota, o.CPUPeriod = nil, nil, nil, nil, nil
	}
	return o
}

// cgroupMax parses one cgroup maximum: a non-negative integer, or "max".
func cgroupMax(s string) *int64 {
	s = strings.TrimSpace(s)
	if s == "max" {
		v := int64(unbounded)
		return &v
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return nil
	}
	return &n
}

// Shortfalls lists every way the observation fails to show what was asked
// for: a requested limit not observed, observed unbounded, or observed looser
// than requested; and network none requested but routes observed, or not
// observed at all. Empty means the workload saw at least the bounds asked for.
func (o ContainerObservation) Shortfalls(limits ExecutionLimits, network string) []string {
	var out []string
	if mib, err := limits.MemoryMiB(); err == nil && mib > 0 {
		want := mib << 20
		if o.Cgroup != "v2" {
			o.MemoryBytes, o.SwapBytes = nil, nil
		}
		out = appendShortfall(out, "memory", o.MemoryBytes, want, strconv.FormatInt(want, 10)+" bytes")
		// Swap is capped at the memory limit: the engine leaves none beyond it.
		out = appendShortfall(out, "swap", o.SwapBytes, 0, "0 bytes beyond memory")
	}
	if milli, err := limits.MilliCPUs(); err == nil && milli > 0 {
		switch {
		case o.Cgroup != "v2" || o.CPUQuota == nil || o.CPUPeriod == nil:
			out = append(out, "cpus: not observed")
		case *o.CPUQuota < 0:
			out = append(out, "cpus: observed unbounded")
		case *o.CPUPeriod <= 0:
			out = append(out, "cpus: invalid observed period")
		case new(big.Int).Mul(big.NewInt(*o.CPUQuota), big.NewInt(1000)).Cmp(new(big.Int).Mul(big.NewInt(milli), big.NewInt(*o.CPUPeriod))) > 0:
			out = append(out, fmt.Sprintf("cpus: observed quota %d/%d, above the %s requested", *o.CPUQuota, *o.CPUPeriod, limits.CPUs))
		}
	}
	if limits.Pids > 0 {
		if o.Cgroup != "v2" {
			o.Pids = nil
		}
		out = appendShortfall(out, "pids", o.Pids, int64(limits.Pids), strconv.Itoa(limits.Pids))
	}
	if network == "none" {
		switch {
		case o.Routes == nil:
			out = append(out, "network none: routes not observed")
		case *o.Routes != 0:
			out = append(out, fmt.Sprintf("network none: observed %d route(s)", *o.Routes))
		}
		switch {
		case o.RoutesIPv6 == nil:
			out = append(out, "network none: IPv6 routes not observed")
		case *o.RoutesIPv6 != 0:
			out = append(out, fmt.Sprintf("network none: observed %d IPv6 route(s)", *o.RoutesIPv6))
		}
	}
	return out
}

func appendShortfall(out []string, name string, got *int64, want int64, wantText string) []string {
	switch {
	case got == nil:
		return append(out, name+": not observed")
	case *got < 0:
		return append(out, name+": observed unbounded")
	case *got > want:
		return append(out, fmt.Sprintf("%s: observed %d, above the %s requested", name, *got, wantText))
	}
	return out
}

// Clone detaches all optional measurements from their source.
func (o ContainerObservation) Clone() ContainerObservation {
	cp := func(p *int64) *int64 {
		if p == nil {
			return nil
		}
		n := *p
		return &n
	}
	o.MemoryBytes, o.SwapBytes, o.Pids = cp(o.MemoryBytes), cp(o.SwapBytes), cp(o.Pids)
	o.CPUQuota, o.CPUPeriod = cp(o.CPUQuota), cp(o.CPUPeriod)
	o.Routes, o.RoutesIPv6 = cp(o.Routes), cp(o.RoutesIPv6)
	return o
}

// BoundedShortfalls checks the initial-state claim made by strict@2: finite,
// positive cgroup v2 maxima, no additional swap, and no non-loopback routes.
// Specific numeric ceilings belong in protected version 3 requirements.
func (o ContainerObservation) BoundedShortfalls() []string {
	out := o.Shortfalls(ExecutionLimits{}, "none")
	if o.Cgroup != "v2" {
		return append(out, "resources: cgroup v2 not observed")
	}
	for _, v := range []struct {
		name  string
		value *int64
	}{
		{"memory", o.MemoryBytes}, {"pids", o.Pids}, {"cpu quota", o.CPUQuota}, {"cpu period", o.CPUPeriod},
	} {
		if v.value == nil || *v.value <= 0 {
			out = append(out, v.name+": positive finite bound not observed")
		}
	}
	if o.SwapBytes == nil || *o.SwapBytes != 0 {
		out = append(out, "swap: zero additional swap not observed")
	}
	return out
}
