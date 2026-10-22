package domain

import (
	"bufio"
	"fmt"
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
	Cgroup string
	// MemoryBytes, SwapBytes and Pids are the cgroup maxima, or -1 for "max"
	// (unbounded). Nil means not observed.
	MemoryBytes *int64
	SwapBytes   *int64
	Pids        *int64
	// CPUQuota and CPUPeriod are cpu.max's two fields; a quota of -1 is "max".
	CPUQuota  *int64
	CPUPeriod *int64
	// Routes counts the IPv4 routes the workload saw; nil means not observed.
	// Both address families must be observed without non-loopback routes for
	// network none. Interface lists are not evidence: kernels add fallback
	// tunnel devices to every namespace.
	Routes *int64
	// RoutesIPv6 counts IPv6 routes outside loopback; nil means not observed.
	RoutesIPv6 *int64
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
		out = appendShortfall(out, "memory", o.MemoryBytes, want, strconv.FormatInt(want, 10)+" bytes")
		// Swap is capped at the memory limit: the engine leaves none beyond it.
		out = appendShortfall(out, "swap", o.SwapBytes, 0, "0 bytes beyond memory")
	}
	if milli, err := limits.MilliCPUs(); err == nil && milli > 0 {
		switch {
		case o.CPUQuota == nil || o.CPUPeriod == nil:
			out = append(out, "cpus: not observed")
		case *o.CPUQuota == unbounded:
			out = append(out, "cpus: observed unbounded")
		case *o.CPUQuota*1000 > milli**o.CPUPeriod:
			out = append(out, fmt.Sprintf("cpus: observed quota %d/%d, above the %s requested", *o.CPUQuota, *o.CPUPeriod, limits.CPUs))
		}
	}
	if limits.Pids > 0 {
		out = appendShortfall(out, "pids", o.Pids, int64(limits.Pids), strconv.Itoa(limits.Pids))
	}
	if network == "none" {
		switch {
		case o.Routes == nil:
			out = append(out, "network none: routes not observed")
		case *o.Routes > 0:
			out = append(out, fmt.Sprintf("network none: observed %d route(s)", *o.Routes))
		}
		switch {
		case o.RoutesIPv6 == nil:
			out = append(out, "network none: IPv6 routes not observed")
		case *o.RoutesIPv6 > 0:
			out = append(out, fmt.Sprintf("network none: observed %d IPv6 route(s)", *o.RoutesIPv6))
		}
	}
	return out
}

func appendShortfall(out []string, name string, got *int64, want int64, wantText string) []string {
	switch {
	case got == nil:
		return append(out, name+": not observed")
	case *got == unbounded:
		return append(out, name+": observed unbounded")
	case *got > want:
		return append(out, fmt.Sprintf("%s: observed %d, above the %s requested", name, *got, wantText))
	}
	return out
}
