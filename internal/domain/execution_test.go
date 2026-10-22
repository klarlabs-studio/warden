package domain

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"
)

func TestExecutionConfig(t *testing.T) {
	for _, cfg := range []ExecutionConfig{{}, {Mode: "host"}, {Runtime: "apple", Image: "checks:dev"}, {Image: "local/checks:dev"}, {Mode: "container", Runtime: "podman", Image: "local/checks:dev", Platform: "linux/arm64", Network: "bridge"}} {
		if err := cfg.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, cfg := range []ExecutionConfig{{Mode: "typo"}, {Mode: "container"}, {Runtime: "docker"}, {Runtime: "shell"}, {Image: "-evil"}, {Image: "bad image"}, {Platform: "windows/amd64"}, {Network: "host"}} {
		if cfg.Validate() == nil {
			t.Fatalf("accepted %+v", cfg)
		}
	}
	base := ExecutionConfig{Mode: "auto", Runtime: "docker", Image: "checks:dev", Platform: "linux/amd64", Network: "none"}
	child := ExecutionConfig{Mode: "host", Network: "bridge"}.OverlayOnto(base)
	if child.ContainerEnabled() || child.Image != base.Image || child.Network != "bridge" {
		t.Fatalf("bad overlay: %+v", child)
	}
	cfg := Config{Execution: ExecutionConfig{Runtime: "podman"}}.OverlayOnto(Config{Execution: base})
	if cfg.Execution.Runtime != "podman" || cfg.Execution.Image != base.Image {
		t.Fatal(cfg.Execution)
	}
}

func TestContainerEvidenceIsSigned(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	id := "sha256:" + strings.Repeat("a", 64)
	rec := RunRecord{CommitSHA: "tip", PublicKey: base64.StdEncoding.EncodeToString(pub), Environments: map[StepName]ExecutionEnvironment{StepTest: {Runtime: "docker", Image: "checks:dev", ImageID: id, Platform: "linux/amd64", Network: "none"}}}
	payload, err := rec.SigningPayload()
	if err != nil {
		t.Fatal(err)
	}
	rec.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))
	if !rec.VerifySignature() || !rec.ContainerChecks([]StepName{StepTest}, id) {
		t.Fatal("valid evidence refused")
	}
	if rec.ContainerChecks([]StepName{StepLint}, id) || rec.ContainerChecks([]StepName{StepTest}, "sha256:"+strings.Repeat("b", 64)) {
		t.Fatal("missing/mismatched evidence accepted")
	}
	env := rec.Environments[StepTest]
	env.ImageID = "sha256:" + strings.Repeat("b", 64)
	rec.Environments[StepTest] = env
	if rec.VerifySignature() {
		t.Fatal("image substitution preserved signature")
	}
	legacy := RunRecord{}
	bytes, err := legacy.SigningPayload()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bytes), "environments") || legacy.ContainerChecks([]StepName{StepTest}, "") {
		t.Fatal("legacy note inferred container execution")
	}
}

func TestAppleEvidenceRequiresLinuxManifestIdentity(t *testing.T) {
	env := ExecutionEnvironment{Runtime: "apple", ImageID: "sha256:" + strings.Repeat("a", 64), ImageIDKind: "manifest", ImageIndexDigest: "sha256:" + strings.Repeat("b", 64), HostOS: "darwin", GuestOS: "linux", Platform: "linux/arm64"}
	for _, mutate := range []func(*ExecutionEnvironment){func(e *ExecutionEnvironment) { e.GuestOS = "darwin" }, func(e *ExecutionEnvironment) { e.ImageIDKind = "config" }, func(e *ExecutionEnvironment) { e.ImageIndexDigest = "" }} {
		altered := env
		mutate(&altered)
		rec := RunRecord{Environments: map[StepName]ExecutionEnvironment{StepTest: altered}}
		if rec.ContainerChecks([]StepName{StepTest}, "") {
			t.Fatal("incomplete Apple provenance accepted", altered)
		}
	}
}

func TestContainerObservationsSignedAndDetached(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	observation := ParseContainerObservation("cgroup=v2\nmemory.max=536870912\nmemory.swap.max=0\ncpu.max=100000 100000\npids.max=128\nroutes=0\nroutes6=0\n")
	run := newTestRun()
	if err := run.RecordStep(StepResult{Step: StepTest, Status: StepPass, Environment: &ExecutionEnvironment{Runtime: "docker"}, Observed: &observation}); err != nil {
		t.Fatal(err)
	}
	rec := RunRecord{CommitSHA: "tip", PublicKey: base64.StdEncoding.EncodeToString(pub), Observations: run.ExecutionObservations()}
	payload, err := rec.SigningPayload()
	if err != nil {
		t.Fatal(err)
	}
	rec.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))
	*observation.MemoryBytes = 1
	if !rec.VerifySignature() {
		t.Fatal("record aliases source observation")
	}
	changed := rec.Observations[StepTest]
	*changed.MemoryBytes = 2
	if rec.VerifySignature() {
		t.Fatal("tampered observation accepted")
	}
	rec.Observations = nil
	if rec.VerifySignature() {
		t.Fatal("stripped observation accepted")
	}
	legacy, _ := (RunRecord{}).SigningPayload()
	if strings.Contains(string(legacy), "observations") {
		t.Fatal("legacy payload changed")
	}
	for _, outcome := range []StepOutcome{StepCached, StepSkipped, StepPredicted} {
		r := newTestRun()
		if err := r.RecordStep(StepResult{Step: StepTest, Status: StepPass, Outcome: outcome, Environment: &ExecutionEnvironment{}, Observed: &observation}); err != nil {
			t.Fatal(err)
		}
		if len(r.ExecutionObservations()) != 0 {
			t.Fatalf("%s was presented as fresh", outcome)
		}
	}
}

func TestObservationCannotAccompanyReuse(t *testing.T) {
	run := newTestRun()
	if err := run.RecordStep(StepResult{Step: StepTest, Status: StepPass, Outcome: StepExecuted, Environment: &ExecutionEnvironment{}, Observed: &ContainerObservation{}, Reuse: &ExecutionReuse{Source: "cache"}}); err != nil {
		t.Fatal(err)
	}
	if len(run.ExecutionObservations()) != 0 {
		t.Fatal("reuse recorded as fresh despite contradictory outcome")
	}
}

func TestObservationAttestationOverlay(t *testing.T) {
	enabled, disabled := true, false
	parent := ExecutionConfig{AttestObservations: &enabled}
	inherited := (ExecutionConfig{}).OverlayOnto(parent)
	if inherited.AttestObservations == nil || !*inherited.AttestObservations {
		t.Fatal("lost parent opt-in")
	}
	overridden := (ExecutionConfig{AttestObservations: &disabled}).OverlayOnto(parent)
	if overridden.AttestObservations == nil || *overridden.AttestObservations {
		t.Fatal("false failed to override parent")
	}
	if !*parent.AttestObservations {
		t.Fatal("overlay mutated parent")
	}
	disabled = true
	if *overridden.AttestObservations {
		t.Fatal("overlay aliases child")
	}
	if (ExecutionConfig{}).OverlayOnto(ExecutionConfig{}).AttestObservations != nil {
		t.Fatal("default unexpectedly enabled")
	}
}
