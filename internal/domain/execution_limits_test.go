package domain

import "testing"

func TestExecutionLimits_Memory(t *testing.T) {
	for in, want := range map[string]int64{"": 0, "512MiB": 512, "4GiB": 4096, " 2 GiB ": 2048, "1TiB": 1 << 20, "6MiB": 6} {
		got, err := ExecutionLimits{Memory: in}.MemoryMiB()
		if err != nil || got != want {
			t.Errorf("MemoryMiB(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"512", "512MB", "4G", "1.5GiB", "-1GiB", "0MiB", "5MiB", "GiB", "4gib", "99999999999TiB"} {
		if _, err := (ExecutionLimits{Memory: in}).MemoryMiB(); err == nil {
			t.Errorf("MemoryMiB(%q) accepted", in)
		}
	}
}

func TestExecutionLimits_CPUs(t *testing.T) {
	for in, want := range map[string]int64{"": 0, "2": 2000, "1.5": 1500, "0.25": 250, "0.001": 1, "16": 16000} {
		got, err := ExecutionLimits{CPUs: in}.MilliCPUs()
		if err != nil || got != want {
			t.Errorf("MilliCPUs(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"0", "0.0", "0.0001", "-1", "+1", ".5", "1.", "1e3", "two", "1.5.0"} {
		if _, err := (ExecutionLimits{CPUs: in}).MilliCPUs(); err == nil {
			t.Errorf("MilliCPUs(%q) accepted", in)
		}
	}
}

func TestExecutionLimits_ValidateAndOverlay(t *testing.T) {
	if err := (ExecutionConfig{Limits: &ExecutionLimits{Memory: "4GiB", CPUs: "2", Pids: 512}}).Validate(); err != nil {
		t.Fatal(err)
	}
	for _, l := range []ExecutionLimits{{Memory: "4G"}, {CPUs: "0"}, {Pids: -1}} {
		if err := (ExecutionConfig{Limits: &l}).Validate(); err == nil {
			t.Errorf("accepted %+v", l)
		}
	}

	base := ExecutionConfig{Image: "checks:dev", Limits: &ExecutionLimits{Memory: "4GiB", CPUs: "2", Pids: 512}}
	// A child that names one limit keeps the others it inherits.
	got := ExecutionConfig{Limits: &ExecutionLimits{Memory: "2GiB"}}.OverlayOnto(base)
	if *got.Limits != (ExecutionLimits{Memory: "2GiB", CPUs: "2", Pids: 512}) {
		t.Errorf("overlay = %+v", *got.Limits)
	}
	if *base.Limits != (ExecutionLimits{Memory: "4GiB", CPUs: "2", Pids: 512}) {
		t.Error("overlay mutated the base's limits")
	}
	// A child that names none keeps the base's.
	if got := (ExecutionConfig{Network: "none"}).OverlayOnto(base); got.Limits != base.Limits {
		t.Errorf("limits lost: %+v", got.Limits)
	}
	// Limits on a child with none in the base.
	if got := (ExecutionConfig{Limits: &ExecutionLimits{Pids: 64}}).OverlayOnto(ExecutionConfig{}); got.Limits == nil || got.Limits.Pids != 64 {
		t.Errorf("limits not set: %+v", got.Limits)
	}
}
