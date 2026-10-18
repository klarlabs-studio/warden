package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// profiledNote gates HEAD under cfg with --attest-only and returns the signed
// note it wrote, after asserting the note attests HEAD and verifies.
func (g *goldenRepo) profiledNote(cfg string) domain.RunRecord {
	g.t.Helper()
	g.writeFile(".warden.yaml", cfg)
	if out, code := g.warden("init"); code != 0 {
		g.t.Fatalf("init: %d %s", code, out)
	}
	if out, code := g.warden("trust", "add"); code != 0 {
		g.t.Fatalf("trust add: %d %s", code, out)
	}
	g.git("add", ".warden.yaml")
	g.git("commit", "--no-verify", "-m", "adopt a profile")
	if out, code := g.warden("run", "pre-push", "--attest-only"); code != 0 {
		g.t.Fatalf("profiled gate: %d %s", code, out)
	}
	var rec domain.RunRecord
	if err := json.Unmarshal([]byte(g.readNote()), &rec); err != nil {
		g.t.Fatalf("parse note: %v", err)
	}
	if !rec.Attests(g.revParse("HEAD")) || !rec.VerifySignature() {
		g.t.Fatalf("the profiled note must attest HEAD and verify: %+v", rec)
	}
	return rec
}

// A real note gated under standard, with every required step configured,
// conforms to standard when CI recomputes it from the signed observations —
// and does not conform to strict, which it was never gated under.
func TestGoldenFleet_MinProfileStandardPassesOnARealNote(t *testing.T) {
	g := newGoldenRepo(t)
	rec := g.profiledNote("hooks: { pre_push: true }\nprofile: standard\n" +
		"steps: { pre_commit: [credentials], pre_push: [credentials, lint, test] }\n" +
		"commands: { lint: \"true\", test: \"true\" }\nrules: []\n")

	// The artifact first: the note records what the verifier will read.
	standard, err := domain.ParseComplianceProfile("standard")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Profile == nil || *rec.Profile != domain.NewProfileRef(standard) {
		t.Fatalf("profile = %+v, want standard@1 with the built-in digest", rec.Profile)
	}
	for _, s := range []domain.StepName{domain.StepCredentials, domain.StepLint, domain.StepTest} {
		if got := rec.StepOutcomes[s].Outcome; got != domain.StepExecuted {
			t.Fatalf("%s outcome = %q, want executed: %+v", s, got, rec.StepOutcomes)
		}
	}

	fp := rec.SignerFingerprint()
	out, code := g.warden("verify", "--key", fp, "--min-profile", "standard")
	if code != 0 {
		t.Fatalf("verify --min-profile standard: %d %s", code, out)
	}
	if !strings.Contains(out, "conforms to standard@1; checked: trusted, commit-bound local provenance") ||
		strings.Contains(out, "not checked by this release") {
		t.Errorf("a standard pass must list what it checked and defer nothing:\n%s", out)
	}

	out, code = g.warden("verify", "--key", fp, "--min-profile", "strict")
	if code != 1 {
		t.Fatalf("a standard note must not conform to strict: %d %s", code, out)
	}
	for _, want := range []string{"gated under standard@1", "security-scan has no recorded outcome", "review has no recorded outcome", "lint has no complete container execution record"} {
		if !strings.Contains(out, want) {
			t.Errorf("strict failure must name %q:\n%s", want, out)
		}
	}

	// The roster in the checkout under test cannot stand in for a pinned key.
	if out, code := g.warden("verify", "--min-profile", "standard"); code != 2 {
		t.Errorf("--min-profile without --key must be refused: %d %s", code, out)
	}
}

// Scenario 9 (F8): checks that ran in a container with network bridge satisfy
// --container-steps, which never looks at the network, but not strict
// conformance, which requires network none for every required shell step.
//
// The run is gated under standard: strict's own floor refuses network bridge
// at load, so no warden can write a strict note with it. --min-profile strict
// must name the network for each check, not only the lower rank.
func TestGoldenFleet_StrictConformanceNeedsNetworkNone(t *testing.T) {
	image := os.Getenv("WARDEN_CONTAINER_TEST_IMAGE")
	if image == "" {
		t.Skip("set WARDEN_CONTAINER_TEST_IMAGE to a preloaded Linux image")
	}
	runtimeName := os.Getenv("WARDEN_CONTAINER_TEST_RUNTIME")
	if runtimeName == "" {
		runtimeName = "docker"
	}
	g := newGoldenRepo(t)
	rec := g.profiledNote(fmt.Sprintf(`hooks: { pre_push: true }
profile: standard
execution: { image: %q, runtime: %q, network: bridge }
steps: { pre_commit: [credentials], pre_push: [credentials, lint, test] }
commands: { lint: "true", test: "true" }
rules: []
`, image, runtimeName))

	for _, s := range []domain.StepName{domain.StepLint, domain.StepTest} {
		env, ok := rec.Environments[s]
		if !ok || env.Network != "bridge" || !domain.ValidContainerImageID(env.ImageID) {
			t.Fatalf("%s must carry a container record with network bridge: %+v", s, rec.Environments)
		}
		if rec.StepOutcomes[s].Outcome != domain.StepExecuted {
			t.Fatalf("%s must have executed: %+v", s, rec.StepOutcomes)
		}
	}
	fp := rec.SignerFingerprint()
	if out, code := g.warden("verify", "--key", fp, "--container-steps", "lint,test"); code != 0 {
		t.Fatalf("--container-steps accepts a bridge-network container record (F8): %d %s", code, out)
	}
	out, code := g.warden("verify", "--key", fp, "--min-profile", "strict")
	if code != 1 {
		t.Fatalf("--min-profile strict must refuse network bridge: %d %s", code, out)
	}
	for _, want := range []string{`lint ran with network "bridge"`, `test ran with network "bridge"`} {
		if !strings.Contains(out, want) {
			t.Errorf("strict failure must name %q:\n%s", want, out)
		}
	}
	if out, code := g.warden("verify", "--key", fp, "--min-profile", "standard"); code != 0 {
		t.Errorf("the same note conforms to standard, which has no network clause: %d %s", code, out)
	}
}
