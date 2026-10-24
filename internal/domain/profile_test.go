package domain

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// Published definitions never change. If one of these fails, a definition was
// edited: revert it and publish the change as a new version instead. A verifier
// compares a note's profile digest with its own table, so editing strict@1 in
// place would make every older note look forged, or every newer one honored by
// an older verifier that means something else by the same name.
//
// ADR 0007 kept one window open: until the release that first wrote the
// profile into signed notes, PRs could extend standard@1 and strict@1 and
// re-pin these digests. That release was 0.37.0, and the window is closed.
const (
	goldenPermissive1 = "sha256:85ae44054d6db52f954f7b575905d58dcabff4793fc52452f3030f1f13bca09d"
	goldenStandard1   = "sha256:2e8d45308adf4c55d9ea918f35673f42b3e1223dc569766dd82bb6bd8937aeb9"
	goldenStrict1     = "sha256:a1cce735c807f3ff038a39d7f87b1c50a42f1ffe3ecba67d72634994b2e94af9"
)

func mustProfile(t *testing.T, s string) ComplianceProfile {
	t.Helper()
	p, err := ParseComplianceProfile(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return p
}

func TestComplianceProfile_DigestsArePinned(t *testing.T) {
	for name, want := range map[string]string{
		"permissive@1": goldenPermissive1,
		"standard@1":   goldenStandard1,
		"strict@1":     goldenStrict1,
		"strict@2":     "sha256:9ed9e755f8c271f4ddf455069ea9f8cb75b99e806df0e9bd22c8eb2b3774f618",
		"strict@3":     "sha256:4605bbf4c0cdd131e5c1bf15d6139c8482673107232982652a23af1ee6c520a5",
	} {
		if got := mustProfile(t, name).Digest(); got != want {
			t.Errorf("%s digest = %s, want %s: a published definition changed", name, got, want)
		}
	}
	if len(builtinProfiles) != 5 {
		t.Errorf("a new profile version needs its own pinned digest here; have %d definitions", len(builtinProfiles))
	}
}

func TestParseComplianceProfile(t *testing.T) {
	cases := []struct {
		in, want string
		level    ProfileLevel
	}{
		{"permissive", "permissive@1", ProfilePermissive},
		{"standard", "standard@1", ProfileStandard},
		{"strict", "strict@1", ProfileStrict},
		{"strict@1", "strict@1", ProfileStrict},
	}
	for _, tc := range cases {
		p := mustProfile(t, tc.in)
		if p.String() != tc.want || p.Level() != tc.level || p.Version() != 1 {
			t.Errorf("%q resolved to %s level %v version %d", tc.in, p, p.Level(), p.Version())
		}
	}
	// A bare name is the same definition as its @1, byte for byte.
	if mustProfile(t, "strict").Digest() != mustProfile(t, "strict@1").Digest() {
		t.Error("bare strict must mean strict@1")
	}
	for _, bad := range []string{"", "Strict", "paranoid", "strict@4", "strict@0", "strict@", "strict@01", "strict@x", "@1", "strict@1@1"} {
		if _, err := ParseComplianceProfile(bad); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
	_, err := ParseComplianceProfile("strict@4")
	if err == nil || !strings.Contains(err.Error(), "newer warden") {
		t.Errorf("an unknown version must say it needs a newer warden: %v", err)
	}
}

func TestComplianceProfile_LevelsAreOrdered(t *testing.T) {
	perm, std, strict := mustProfile(t, "permissive"), mustProfile(t, "standard"), mustProfile(t, "strict")
	if !perm.Lowers(std) || !std.Lowers(strict) || !perm.Lowers(strict) {
		t.Error("permissive < standard < strict must hold")
	}
	if strict.Lowers(std) || strict.Lowers(strict) {
		t.Error("strict lowers nothing at or below it")
	}
}

func TestComplianceProfile_Base(t *testing.T) {
	if b := mustProfile(t, "permissive").Base(); b.Signing.Required || len(b.Timeouts) != 0 || b.Execution != (ExecutionConfig{}) {
		t.Errorf("permissive@1 sets no defaults: %+v", b)
	}
	for _, name := range []string{"standard", "strict"} {
		b := mustProfile(t, name).Base()
		if !b.Signing.Required {
			t.Errorf("%s defaults signing.required to true", name)
		}
		for _, s := range []StepName{StepIntent, StepReview, StepDocument} {
			if b.Timeouts[string(s)] != DefaultAgentTimeout {
				t.Errorf("%s: agent step %s timeout default = %q", name, s, b.Timeouts[string(s)])
			}
		}
		if len(b.Steps) != 0 {
			t.Errorf("%s: Base must not set steps (a union would reorder the repo's own)", name)
		}
	}
	if n := mustProfile(t, "strict").Base().Execution.Network; n != "none" {
		t.Errorf("strict defaults execution.network to none, got %q", n)
	}
	if n := mustProfile(t, "standard").Base().Execution.Network; n != "" {
		t.Errorf("standard leaves execution.network alone, got %q", n)
	}
}

func TestComplianceProfile_ApplyLayersChainOverBase(t *testing.T) {
	p := mustProfile(t, "strict")
	chain := Config{
		Profile:  "strict",
		Timeouts: map[string]string{"review": "3m"},
		Signing:  SigningConfig{Required: false, Signer: "ssh"},
		Rules:    []Rule{{Then: Then{Agent: map[StepName]string{"audit": "claude"}}}},
	}
	got := p.Apply(chain)
	if got.Profile != "strict@1" {
		t.Errorf("effective profile is canonical: %q", got.Profile)
	}
	if got.Timeouts["review"] != "3m" || got.Timeouts["intent"] != DefaultAgentTimeout {
		t.Errorf("the chain's timeout wins, the default fills the rest: %v", got.Timeouts)
	}
	if got.Timeouts["audit"] != DefaultAgentTimeout {
		t.Errorf("a rule-assigned agent step gets the default timeout too: %v", got.Timeouts)
	}
	if !got.Signing.Required || got.Signing.Signer != "ssh" {
		t.Errorf("signing.required ratchets from the base; signer stays the repo's: %+v", got.Signing)
	}
	if got.Execution.Network != "none" {
		t.Errorf("strict's network default must apply: %q", got.Execution.Network)
	}
	if chain.Timeouts["intent"] != "" {
		t.Error("Apply must not mutate the chain it was given")
	}
}

// compliant returns an effective config meeting every floor of strict@1.
func compliant() Config {
	return Config{
		Execution: ExecutionConfig{Mode: "container", Image: "golang:1.26", Network: "none"},
		Commands:  map[string]string{"lint": "make lint", "test": "make test", "security-scan": "nox scan ."},
		Steps: map[string][]StepName{
			"pre_commit": {StepCredentials, StepLint},
			"pre_push":   {StepRebase, StepReview, StepCredentials, StepLint, StepTest, StepSecurityScan},
		},
		Timeouts: map[string]string{"intent": "10m", "review": "10m", "document": "10m"},
		Signing:  SigningConfig{Required: true},
	}
}

func floorsOf(vs []FloorViolation) []string {
	var out []string
	for _, v := range vs {
		out = append(out, v.Floor+" "+v.Field)
	}
	return out
}

func TestComplianceProfile_FloorsPerLevel(t *testing.T) {
	for _, name := range []string{"permissive", "standard", "strict"} {
		if vs := mustProfile(t, name).Check(compliant()); len(vs) != 0 {
			t.Errorf("%s: a compliant config must pass, got %v", name, floorsOf(vs))
		}
	}
	// permissive has no floors at all: even an empty config passes.
	if vs := mustProfile(t, "permissive").Check(Config{}); len(vs) != 0 {
		t.Errorf("permissive@1 has no floors, got %v", floorsOf(vs))
	}
	if len(mustProfile(t, "permissive").Floors()) != 0 {
		t.Error("permissive@1 lists no floors")
	}
}

func TestComplianceProfile_EachFloor(t *testing.T) {
	cases := []struct {
		name    string
		profile string
		mutate  func(*Config)
		want    string // "floor field" of the single expected violation
	}{
		{"standard: pre_push lacks test", "standard", func(c *Config) {
			c.Steps["pre_push"] = []StepName{StepCredentials, StepLint}
		}, "required-steps steps.pre_push"},
		{"standard: pre_commit default lacks credentials", "standard", func(c *Config) {
			delete(c.Steps, "pre_commit")
		}, "required-steps steps.pre_commit"},
		{"strict: pre_push lacks review", "strict", func(c *Config) {
			c.Steps["pre_push"] = []StepName{StepCredentials, StepLint, StepTest, StepSecurityScan}
		}, "required-steps steps.pre_push"},
		{"standard: a rule skips test", "standard", func(c *Config) {
			c.Rules = []Rule{{Then: Then{Steps: map[string]StepEdit{"pre_push": {Skip: []StepName{StepTest}}}}}}
		}, "no-skip-required rules[0].then.steps.pre_push.skip"},
		{"standard: skipping an optional step is fine", "standard", func(c *Config) {
			c.Rules = []Rule{{Then: Then{Steps: map[string]StepEdit{"pre_push": {Skip: []StepName{StepRebase}}}}}}
		}, ""},
		{"standard: lint has no command", "standard", func(c *Config) {
			c.Commands["lint"] = "  "
		}, "commands-present commands.lint"},
		{"standard: security-scan command is not required", "standard", func(c *Config) {
			delete(c.Commands, "security-scan")
		}, ""},
		{"strict: security-scan has no command", "strict", func(c *Config) {
			delete(c.Commands, "security-scan")
		}, "commands-present commands.security-scan"},
		{"strict: host execution", "strict", func(c *Config) {
			c.Execution.Mode = "host"
		}, "container-execution execution.mode"},
		{"strict: no image", "strict", func(c *Config) {
			c.Execution = ExecutionConfig{Network: "none"}
		}, "container-execution execution.image"},
		{"standard: host execution is allowed", "standard", func(c *Config) {
			c.Execution = ExecutionConfig{Mode: "host", Network: "bridge"}
		}, ""},
		{"strict: bridge network", "strict", func(c *Config) {
			c.Execution.Network = "bridge"
		}, "network-none execution.network"},
		{"strict: auto_fix on review", "strict", func(c *Config) {
			c.Rules = []Rule{{Then: Then{AutoFix: map[StepName]int{StepReview: 2}}}}
		}, "no-agent-writes rules[0].then.auto_fix.review"},
		{"strict: auto_fix on a rule-assigned agent step", "strict", func(c *Config) {
			c.Rules = []Rule{
				{Then: Then{Agent: map[StepName]string{"audit": "claude"}}},
				{Then: Then{AutoFix: map[StepName]int{"audit": 1}}},
			}
			c.Timeouts["audit"] = "5m"
		}, "no-agent-writes rules[1].then.auto_fix.audit"},
		{"strict: auto_fix on lint is allowed", "strict", func(c *Config) {
			c.Rules = []Rule{{Then: Then{AutoFix: map[StepName]int{StepLint: 2}}}}
		}, ""},
		{"strict: writes lists document", "strict", func(c *Config) {
			c.Writes = []string{"document"}
		}, "no-agent-writes writes"},
		{"standard: auto_fix on review is allowed", "standard", func(c *Config) {
			c.Rules = []Rule{{Then: Then{AutoFix: map[StepName]int{StepReview: 2}}}}
		}, ""},
		{"strict: review timeout 0", "strict", func(c *Config) {
			c.Timeouts["review"] = "0"
		}, "agent-timeout timeouts.review"},
		{"standard: review timeout 0 is allowed", "standard", func(c *Config) {
			c.Timeouts["review"] = "0s"
		}, ""},
		{"standard: signing not required", "standard", func(c *Config) {
			c.Signing.Required = false
		}, "signing-required signing.required"},
		{"strict: a custom top-level agent", "strict", func(c *Config) {
			c.Agent = "my-agent"
		}, "bundled-agents agent"},
		{"strict: agent auto is not a preset", "strict", func(c *Config) {
			c.Agent = "auto"
		}, "bundled-agents agent"},
		{"strict: a rule assigns a custom agent", "strict", func(c *Config) {
			c.Rules = []Rule{{Then: Then{Agent: map[StepName]string{StepReview: "faux"}}}}
		}, "bundled-agents rules[0].then.agent.review"},
		{"strict: a bundled agent with its command overridden is allowed", "strict", func(c *Config) {
			c.Agent = "codex"
			c.AgentCommands = map[string]string{"codex": "my-codex-wrapper {prompt}"}
		}, ""},
		{"standard: a custom agent is allowed", "standard", func(c *Config) {
			c.Agent = "my-agent"
			c.Rules = []Rule{{Then: Then{Agent: map[StepName]string{StepReview: "faux"}}}}
		}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := compliant()
			tc.mutate(&cfg)
			got := floorsOf(mustProfile(t, tc.profile).Check(cfg))
			if tc.want == "" {
				if len(got) != 0 {
					t.Errorf("want no violation, got %v", got)
				}
				return
			}
			if len(got) != 1 || got[0] != tc.want {
				t.Errorf("violations = %v, want [%s]", got, tc.want)
			}
		})
	}
}

// What each level enforces around agent steps at run time (ADR 0007 §3).
func TestComplianceProfile_AgentBoundary(t *testing.T) {
	for _, tc := range []struct {
		profile                  string
		require, isolate, envAll bool
	}{
		{"permissive", false, false, false},
		{"standard", true, true, false},
		{"strict", true, true, true},
	} {
		b := mustProfile(t, tc.profile).AgentBoundary()
		if b.RequireExecution != tc.require || b.Isolate != tc.isolate || b.EnvAllowlist != tc.envAll {
			t.Errorf("%s: boundary %+v", tc.profile, b)
		}
		if b.Profile != tc.profile+"@1" {
			t.Errorf("%s: boundary names %q", tc.profile, b.Profile)
		}
	}
}

func TestResolvedPolicy_AgentBoundaryFor(t *testing.T) {
	strict := ResolvedPolicy{Profile: &ResolvedProfile{Name: "strict@1"}, Agents: map[StepName]string{"audit": "claude"}}
	for _, s := range []StepName{StepReview, StepIntent, StepDocument, "audit"} {
		if b := strict.AgentBoundaryFor(s); !b.Isolate || !b.EnvAllowlist || !b.RequireExecution {
			t.Errorf("strict %s: %+v", s, b)
		}
	}
	if b := strict.AgentBoundaryFor(StepLint); b.Active() {
		t.Errorf("lint is not an agent step: %+v", b)
	}
	for _, p := range []ResolvedPolicy{{}, {Profile: &ResolvedProfile{Name: "permissive@1"}}} {
		if b := p.AgentBoundaryFor(StepReview); b.Active() || b != (AgentBoundary{}) {
			t.Errorf("profile %+v must enforce nothing: %+v", p.Profile, b)
		}
	}
}

func TestComplianceProfile_ViolationNamesItsSource(t *testing.T) {
	cfg := compliant()
	cfg.Execution.Mode = "host"
	vs := mustProfile(t, "strict").Check(cfg)
	if len(vs) != 1 || vs[0].SetBy == nil {
		t.Fatalf("host execution must be attributable: %+v", vs)
	}
	if !vs[0].SetBy(Config{Execution: ExecutionConfig{Mode: "host"}}) || vs[0].SetBy(Config{}) {
		t.Error("SetBy must pick out exactly the file that set mode: host")
	}
}

func TestComplianceProfile_FloorStatus(t *testing.T) {
	p := mustProfile(t, "strict")
	cfg := compliant()
	cfg.Execution.Network = "bridge"
	sts := p.FloorStatus(cfg)
	if len(sts) != len(p.Floors()) {
		t.Fatalf("one status per floor: %d vs %d", len(sts), len(p.Floors()))
	}
	for _, st := range sts {
		want := st.ID != FloorNetworkNone
		if st.Satisfied != want {
			t.Errorf("floor %s satisfied = %t, want %t (%v)", st.ID, st.Satisfied, want, st.Problems)
		}
	}
	// It must marshal: MCP policy_explain returns it as JSON.
	if _, err := json.Marshal(sts); err != nil {
		t.Errorf("floor status must marshal: %v", err)
	}
}

func TestOverlayOnto_ProfileRatchets(t *testing.T) {
	cases := []struct{ base, child, want string }{
		{"", "", ""},
		{"strict", "", "strict"},
		{"", "standard", "standard"},
		{"standard", "strict", "strict"},
		{"strict", "standard", "strict"}, // the loader refuses this; the merge never lowers
		{"strict", "permissive", "strict"},
		{"strict", "strict@1", "strict@1"},
	}
	for _, tc := range cases {
		got := Config{Profile: tc.child}.OverlayOnto(Config{Profile: tc.base}).Profile
		if got != tc.want {
			t.Errorf("base %q child %q: profile = %q, want %q", tc.base, tc.child, got, tc.want)
		}
	}
}

func TestConfigValidate_Profile(t *testing.T) {
	for _, ok := range []string{"", "permissive", "standard", "strict", "strict@1"} {
		if err := (Config{Profile: ok}).Validate(); err != nil {
			t.Errorf("profile %q must validate: %v", ok, err)
		}
	}
	for _, bad := range []string{"paranoid", "strict@4"} {
		if err := (Config{Profile: bad}).Validate(); err == nil {
			t.Errorf("profile %q must be a load error", bad)
		}
	}
}

// The configuration digest is part of signed execution contracts, so a config
// without a profile must marshal exactly as it did before the field existed.
func TestConfig_UnprofiledJSONHasNoProfileKey(t *testing.T) {
	b, err := json.Marshal(Config{Agent: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "Profile") {
		t.Errorf("an unprofiled config must not marshal a Profile key: %s", b)
	}
	b, _ = json.Marshal(Config{Profile: "strict@1"})
	if !strings.Contains(string(b), `"Profile":"strict@1"`) {
		t.Errorf("a profiled config must carry it in its digest: %s", b)
	}
}

func TestEffectiveHookSteps(t *testing.T) {
	if got := EffectiveHookSteps(Config{}, PrePush); !slices.Equal(got, DefaultSteps(PrePush)) {
		t.Errorf("an omitted hook uses the defaults: %v", got)
	}
	cfg := Config{Steps: map[string][]StepName{"pre_push": {}}}
	if got := EffectiveHookSteps(cfg, PrePush); len(got) != 0 {
		t.Errorf("an explicitly empty list stays empty: %v", got)
	}
}

// Only standard and strict withhold approval from the agent surface;
// permissive records a profile without changing who may approve.
func TestComplianceProfile_AgentSurfaceMayApprove(t *testing.T) {
	for name, want := range map[string]bool{
		"permissive": true,
		"standard":   false,
		"strict":     false,
	} {
		if got := mustProfile(t, name).AgentSurfaceMayApprove(); got != want {
			t.Errorf("%s: AgentSurfaceMayApprove() = %v, want %v", name, got, want)
		}
	}
}

func TestComplianceProfileVerifierAccessors(t *testing.T) {
	strict, _ := ParseComplianceProfile("strict")
	if got := JoinSteps(strict.RequiredShellSteps(PrePush)); got != "lint, test, security-scan" {
		t.Errorf("strict required shell steps = %q", got)
	}
	if !strict.RequiresContainer() || !strict.RequiresNetworkNone() {
		t.Error("strict requires a network-less container")
	}
	standard, _ := ParseComplianceProfile("standard")
	if standard.RequiresContainer() || standard.RequiresNetworkNone() {
		t.Error("standard requires no container")
	}
	if got, ok := LookupComplianceProfile("strict", 1); !ok || got.Digest() != strict.Digest() {
		t.Error("lookup of strict@1 must return the built-in definition")
	}
	for _, c := range []struct {
		name    string
		version int
	}{{"strict", 4}, {"paranoid", 1}, {"", 1}, {"strict", 0}} {
		if _, ok := LookupComplianceProfile(c.name, c.version); ok {
			t.Errorf("lookup of %s@%d must fail", c.name, c.version)
		}
	}
}

func TestStrictV2Floors(t *testing.T) {
	p := mustProfile(t, "strict@2")
	cfg := p.Apply(compliant())
	if !p.RequiresObservedBounds() || !mustProfile(t, "strict").Lowers(p) {
		t.Fatal("version ordering or bounds missing")
	}
	if got := p.Check(cfg); len(got) != 3 {
		t.Fatalf("missing limits: %v", got)
	}
	cfg.Execution.Limits = &ExecutionLimits{Memory: "512MiB", CPUs: "1.5", Pids: 128}
	if got := p.Check(cfg); len(got) != 0 {
		t.Fatalf("valid config: %v", got)
	}
	cfg.PerWorkspace = map[string]PerWorkspaceStep{"test": {}}
	if got := p.Check(cfg); len(got) != 1 || got[0].Field != "per_workspace" {
		t.Fatalf("workspace aggregate accepted: %v", got)
	}
	cfg.PerWorkspace = nil
	disabled := false
	cfg.Execution.AttestObservations = &disabled
	if got := p.Check(cfg); len(got) != 1 || got[0].Field != "execution.attest_observations" {
		t.Fatalf("opt-out accepted: %v", got)
	}
}

// strict@3 completes ADR 0008's accepted decision 4, which strict@2 shipped
// without: every required shell step needs a finite timeout. warden applies
// that deadline itself, so it is an observation rather than a claim.
func TestStrictV3RequiresShellTimeouts(t *testing.T) {
	p := mustProfile(t, "strict@3")
	v2 := mustProfile(t, "strict@2")
	if !p.RequiresShellTimeout() || v2.RequiresShellTimeout() || !v2.Lowers(p) {
		t.Fatal("strict@3 must require shell timeouts and rank above strict@2, which does not")
	}
	if !p.RequiresObservedBounds() {
		t.Fatal("strict@3 keeps strict@2's observed bounds")
	}

	cfg := p.Apply(compliant())
	cfg.Execution.Limits = &ExecutionLimits{Memory: "512MiB", CPUs: "1.5", Pids: 128}
	var missing []string
	for _, v := range p.Check(cfg) {
		if v.Floor != FloorShellTimeout {
			t.Fatalf("unexpected floor: %v", v)
		}
		missing = append(missing, v.Field)
	}
	if want := []string{"timeouts.lint", "timeouts.test", "timeouts.security-scan"}; !slices.Equal(missing, want) {
		t.Fatalf("shell steps without a timeout = %v, want %v", missing, want)
	}

	cfg.Timeouts["lint"], cfg.Timeouts["test"], cfg.Timeouts["security-scan"] = "5m", "0", "10m"
	if got := p.Check(cfg); len(got) != 1 || got[0].Field != "timeouts.test" || !strings.Contains(got[0].Message, "no limit") {
		t.Fatalf("a timeout of 0 must be refused: %v", got)
	}

	cfg.Timeouts["test"] = "15m"
	if got := p.Check(cfg); len(got) != 0 {
		t.Fatalf("every required shell step bounded: %v", got)
	}
	if got := v2.Check(v2.Apply(compliant())); slices.ContainsFunc(got, func(v FloorViolation) bool { return v.Floor == FloorShellTimeout }) {
		t.Fatalf("strict@2 gained the timeout floor: %v", got)
	}
}
