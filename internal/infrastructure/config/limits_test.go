package config

import (
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// execution.limits loads, and a misspelled limit is refused like any unknown key
// rather than silently leaving the resource unbounded.
func TestParse_ExecutionLimits(t *testing.T) {
	cfg, err := Parse([]byte("execution:\n  image: checks:dev\n  limits: { memory: 4GiB, cpus: \"1.5\", pids: 256 }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if l := cfg.Execution.Limits; l == nil || *l != (domain.ExecutionLimits{Memory: "4GiB", CPUs: "1.5", Pids: 256}) {
		t.Fatalf("limits = %+v", l)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid limits refused: %v", err)
	}
	if _, err := Parse([]byte("execution:\n  limits: { memroy: 4GiB }\n")); err == nil || !strings.Contains(err.Error(), "memroy") {
		t.Fatalf("a misspelled limit must be refused, got %v", err)
	}
	bad, err := Parse([]byte("execution:\n  image: checks:dev\n  limits: { memory: 4G }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "execution.limits.memory") {
		t.Fatalf("an invalid memory limit must fail validation naming the key, got %v", err)
	}
}

func TestParse_ObservationAttestation(t *testing.T) {
	for _, value := range []string{"true", "false"} {
		cfg, err := Parse([]byte("execution:\n  attest_observations: " + value + "\n"))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Execution.AttestObservations == nil || *cfg.Execution.AttestObservations != (value == "true") {
			t.Fatalf("lost explicit %s", value)
		}
	}
	if _, err := Parse([]byte("execution:\n  attest_observations: definitely\n")); err == nil {
		t.Fatal("accepted invalid opt-in")
	}
}
