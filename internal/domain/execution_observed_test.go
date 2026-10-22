package domain

import (
	"strings"
	"testing"
)

const observedLimited = "cgroup=v2\nmemory.max=536870912\nmemory.swap.max=0\ncpu.max=150000 100000\npids.max=128\nroutes=0\nroutes6=0\n"

func TestParseContainerObservation(t *testing.T) {
	o := ParseContainerObservation(observedLimited)
	if o.Cgroup != "v2" || *o.MemoryBytes != 536870912 || *o.SwapBytes != 0 || *o.CPUQuota != 150000 || *o.CPUPeriod != 100000 || *o.Pids != 128 || *o.Routes != 0 {
		t.Fatalf("observation = %+v", o)
	}
	open := ParseContainerObservation("cgroup=v2\nmemory.max=max\ncpu.max=max 100000\npids.max=max\nroutes=2\n")
	if *open.MemoryBytes != unbounded || *open.CPUQuota != unbounded || *open.Pids != unbounded || *open.Routes != 2 {
		t.Fatalf("unbounded observation = %+v", open)
	}
	// Outside cgroup v2 no limit is trusted, but routes still are.
	v1 := ParseContainerObservation("memory.max=536870912\npids.max=128\nroutes=0\nroutes6=0\n")
	if v1.MemoryBytes != nil || v1.Pids != nil || v1.Routes == nil {
		t.Fatalf("non-v2 limits must not be observed: %+v", v1)
	}
	bad := ParseContainerObservation("cgroup=v2\nmemory.max=lots\ncpu.max=150000\npids.max=-3\nroutes=x\n")
	if bad.MemoryBytes != nil || bad.CPUQuota != nil || bad.Pids != nil || bad.Routes != nil {
		t.Fatalf("malformed values must stay unobserved: %+v", bad)
	}
}

func TestContainerObservation_Shortfalls(t *testing.T) {
	asked := ExecutionLimits{Memory: "512MiB", CPUs: "1.5", Pids: 128}
	if got := ParseContainerObservation(observedLimited).Shortfalls(asked, "none"); len(got) != 0 {
		t.Fatalf("an observation meeting the request has no shortfalls: %q", got)
	}
	// Tighter than asked is fine.
	tighter := "cgroup=v2\nmemory.max=268435456\nmemory.swap.max=0\ncpu.max=50000 100000\npids.max=64\nroutes=0\nroutes6=0\n"
	if got := ParseContainerObservation(tighter).Shortfalls(asked, "none"); len(got) != 0 {
		t.Fatalf("tighter bounds are not shortfalls: %q", got)
	}
	cases := []struct {
		name, raw, want string
	}{
		{"engine ignored the limits", "cgroup=v2\nmemory.max=max\nmemory.swap.max=max\ncpu.max=max 100000\npids.max=max\nroutes=0\nroutes6=0\n", "memory: observed unbounded"},
		{"looser memory", "cgroup=v2\nmemory.max=1073741824\nmemory.swap.max=0\ncpu.max=150000 100000\npids.max=128\nroutes=0\nroutes6=0\n", "memory: observed 1073741824"},
		{"swap left", "cgroup=v2\nmemory.max=536870912\nmemory.swap.max=536870912\ncpu.max=150000 100000\npids.max=128\nroutes=0\nroutes6=0\n", "swap: observed 536870912"},
		{"looser cpus", "cgroup=v2\nmemory.max=536870912\nmemory.swap.max=0\ncpu.max=200000 100000\npids.max=128\nroutes=0\nroutes6=0\n", "cpus: observed quota 200000/100000"},
		{"cgroup v1", "routes=0\nroutes6=0\n", "memory: not observed"},
		{"routes present", "cgroup=v2\nmemory.max=536870912\nmemory.swap.max=0\ncpu.max=150000 100000\npids.max=128\nroutes=1\n", "network none: observed 1 route(s)"},
		{"nothing observed", "", "network none: routes not observed"},
	}
	for _, c := range cases {
		got := strings.Join(ParseContainerObservation(c.raw).Shortfalls(asked, "none"), "; ")
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: shortfalls %q, want %q", c.name, got, c.want)
		}
	}
	// Nothing asked, nothing short.
	if got := (ContainerObservation{}).Shortfalls(ExecutionLimits{}, "bridge"); len(got) != 0 {
		t.Fatalf("no request, no shortfall: %q", got)
	}
}

func TestContainerObservationRequiresBothRouteFamilies(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"routes=0\nroutes6=1\n", "observed 1 IPv6 route(s)"},
		{"routes=0\n", "IPv6 routes not observed"},
		{"routes6=0\n", "routes not observed"},
		{"routes=0\nroutes6=invalid\n", "IPv6 routes not observed"},
	} {
		got := strings.Join(ParseContainerObservation(tc.raw).Shortfalls(ExecutionLimits{}, "none"), "; ")
		if !strings.Contains(got, tc.want) {
			t.Errorf("%q: shortfalls %q, want %q", tc.raw, got, tc.want)
		}
	}
	if got := ParseContainerObservation("routes=0\nroutes6=0\n").Shortfalls(ExecutionLimits{}, "none"); len(got) != 0 {
		t.Fatalf("isolated: %q", got)
	}
}
