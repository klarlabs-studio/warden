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
	rec := g.profiledNote(standardConfig("agent: claude\nagent_commands: { claude: \"true\" }\n"))

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
	// The review ran in a guarded clone: the note says so, and says nothing
	// about the environment, which standard does not scrub.
	review := rec.StepOutcomes[domain.StepReview]
	if review.Agent != "claude" || review.Isolated == nil || !*review.Isolated ||
		review.TreeUnchanged == nil || !*review.TreeUnchanged || review.EnvAllowlisted != nil {
		t.Fatalf("review observation = %+v, want claude, isolated, tree unchanged, no environment claim", review)
	}

	fp := rec.SignerFingerprint()
	out, code := g.warden("verify", "--key", fp, "--min-profile", "standard")
	if code != 0 {
		t.Fatalf("verify --min-profile standard: %d %s", code, out)
	}
	if !strings.Contains(out, "conforms to standard@1; checked: trusted, commit-bound local provenance") ||
		!strings.Contains(out, "isolated in a clone") ||
		strings.Contains(out, "not checked by this release") {
		t.Errorf("a standard pass must list what it checked and defer nothing:\n%s", out)
	}

	out, code = g.warden("verify", "--key", fp, "--min-profile", "strict")
	if code != 1 {
		t.Fatalf("a standard note must not conform to strict: %d %s", code, out)
	}
	for _, want := range []string{"gated under standard@1", "security-scan has no recorded outcome", "review has no allowlisted-environment observation", "lint has no complete container execution record"} {
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

// A real strict note, with its checks in a network-less container and its
// review run by an agent in a guarded clone with the allowlisted environment,
// conforms to strict. The pass still names the one clause it cannot check.
func TestGoldenFleet_MinProfileStrictPassesOnARealStrictNote(t *testing.T) {
	g := newGoldenRepo(t)
	rec := g.profiledNote(strictContainerConfig(t, "agent: claude\nagent_commands: { claude: \"true\" }\n"))
	review := rec.StepOutcomes[domain.StepReview]
	for name, v := range map[string]*bool{"isolated": review.Isolated, "tree_unchanged": review.TreeUnchanged, "env_allowlisted": review.EnvAllowlisted} {
		if v == nil || !*v {
			t.Fatalf("review %s must be observed true under strict: %+v", name, review)
		}
	}
	out, code := g.warden("verify", "--key", rec.SignerFingerprint(), "--min-profile", "strict")
	if code != 0 {
		t.Fatalf("a real strict note must conform to strict: %d %s", code, out)
	}
	for _, want := range []string{"conforms to strict@1", "network none", "allowlisted environment", "carries a notarized Agent Trace"} {
		if !strings.Contains(out, want) {
			t.Errorf("strict pass must mention %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "not checked by this release") {
		t.Errorf("strict checks every clause it defines, so nothing is deferred:\n%s", out)
	}
}

// permissiveConfig is standardConfig's steps under the permissive profile:
// the same checks run, but the note records permissive@1.
const permissiveConfig = `hooks: { pre_push: true }
profile: permissive
commands: { lint: "true", test: "true" }
steps:
  pre_commit: [credentials]
  pre_push: [credentials, lint, test]
rules: []
`

// gateHeadUnder commits cfg as .warden.yaml and gates the new HEAD with
// --attest-only, returning the signed note it wrote.
func (g *goldenRepo) gateHeadUnder(cfg, msg string) domain.RunRecord {
	g.t.Helper()
	g.writeFile(".warden.yaml", cfg)
	g.git("add", ".warden.yaml")
	g.git("commit", "--no-verify", "-m", msg)
	if out, code := g.warden("run", "pre-push", "--attest-only"); code != 0 {
		g.t.Fatalf("gate %q: %d %s", msg, code, out)
	}
	var rec domain.RunRecord
	if err := json.Unmarshal([]byte(g.readNote()), &rec); err != nil {
		g.t.Fatalf("parse note: %v", err)
	}
	if !rec.Attests(g.revParse("HEAD")) || !rec.VerifySignature() {
		g.t.Fatalf("the note must attest HEAD and verify: %+v", rec)
	}
	return rec
}

// Scenario 8 of ADR 0007: the range gate reads the minimum profile from the
// BASE ref. The base is standard; the head lowers `profile:` to permissive in
// its own .warden.yaml and is gated under it. The range gate fails it against
// standard — the profile of the branch it would merge into — and with no base
// profile at all it enforces none.
func TestGoldenFleet_RangeGateUsesBaseProfile(t *testing.T) {
	g := newGoldenRepo(t)
	root := g.revParse("HEAD")
	baseRec := g.profiledNote(standardConfig("agent: claude\nagent_commands: { claude: \"true\" }\n"))
	base := g.revParse("HEAD")

	lowerRec := g.gateHeadUnder(permissiveConfig, "lower the profile")
	lowered := g.revParse("HEAD")
	permissive, err := domain.ParseComplianceProfile("permissive")
	if err != nil {
		t.Fatal(err)
	}
	// The artifact first: the head's own note really says permissive.
	if lowerRec.Profile == nil || *lowerRec.Profile != domain.NewProfileRef(permissive) {
		t.Fatalf("head note profile = %+v, want permissive@1", lowerRec.Profile)
	}
	keys := baseRec.SignerFingerprint() + "," + lowerRec.SignerFingerprint()

	// Bootstrap: the root names no profile, so root..base enforces none, and
	// the output must not imply one was checked.
	out, code := g.warden("verify", "--range", root+".."+base, "--key", keys)
	if code != 0 {
		t.Fatalf("a range from a base with no profile is the gate as before: %d %s", code, out)
	}
	if strings.Contains(out, "profile") || strings.Contains(out, "conforms") {
		t.Errorf("no profile applied, so none may be reported:\n%s", out)
	}

	// The head lowered the profile; the base's standard still applies.
	out, code = g.warden("verify", "--range", base+"..HEAD", "--key", keys)
	if code != 1 {
		t.Fatalf("a head gated under permissive must fail a standard base: %d %s", code, out)
	}
	for _, want := range []string{
		"requiring compliance profile standard@1 (from " + base + ":.warden.yaml)",
		"✗ " + lowered[:12],
		"profile standard@1: recorded profile ranks at least standard@1: the note was gated under permissive@1",
		"conforming to standard@1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("range failure must say %q:\n%s", want, out)
		}
	}

	// The same verdict, structured.
	out, code = g.warden("verify", "--range", base+"..HEAD", "--key", keys, "--json")
	if code != 1 {
		t.Fatalf("--json keeps the exit contract: %d %s", code, out)
	}
	var export struct {
		Profiles []struct {
			Profile  string `json:"profile"`
			FromBase bool   `json:"from_base"`
			FromFlag bool   `json:"from_flag"`
		} `json:"profiles"`
		Commits []domain.CommitVerdict `json:"commits"`
	}
	if err := json.Unmarshal([]byte(out), &export); err != nil {
		t.Fatalf("parse --json: %v\n%s", err, out)
	}
	if len(export.Profiles) != 1 || export.Profiles[0].Profile != "standard@1" || !export.Profiles[0].FromBase || export.Profiles[0].FromFlag {
		t.Errorf("profiles = %+v, want standard@1 from the base", export.Profiles)
	}
	if len(export.Commits) != 1 || export.Commits[0].Reason != domain.ReasonProfile || len(export.Commits[0].ProfileFailures) == 0 {
		t.Errorf("commits = %+v, want the lowered commit failing on its profile", export.Commits)
	}

	// The other direction: once permissive is the base, a head gated under
	// standard is judged against permissive — the base's — and passes.
	headRec := g.gateHeadUnder(standardConfig("agent: claude\nagent_commands: { claude: \"true\" }\n"), "raise it again")
	out, code = g.warden("verify", "--range", lowered+"..HEAD", "--key", keys+","+headRec.SignerFingerprint())
	if code != 0 {
		t.Fatalf("a standard note conforms to a permissive base: %d %s", code, out)
	}
	for _, want := range []string{"requiring compliance profile permissive@1 (from " + lowered + ":.warden.yaml)", "every noted commit conforms to permissive@1"} {
		if !strings.Contains(out, want) {
			t.Errorf("range pass must say %q:\n%s", want, out)
		}
	}
}
