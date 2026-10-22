package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/policy"
)

// strictBase is an org base that meets every strict@1 floor on its own.
const strictBase = `profile: strict
execution: { mode: container, image: "golang:1.26" }
commands: { lint: "make lint", test: "make test", security-scan: "nox scan ." }
steps:
  pre_commit: [credentials, lint]
  pre_push: [rebase, review, credentials, lint, test, security-scan]
`

// writeChain writes files (repo-relative path → content) into a fresh repo dir.
func writeChain(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// loadBoth loads the chain from the working tree and through ResolveAtRef, the
// two paths that must agree.
func loadBoth(t *testing.T, dir string) (domain.Config, error, domain.Config, error) {
	t.Helper()
	wt, wtErr := NewRepository(dir).Load()
	ref, refErr := ResolveAtRef(func(rel string) ([]byte, bool, error) {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return data, err == nil, err
	})
	return wt, wtErr, ref, refErr
}

func TestLoad_ProfileBaseIsTheLowestLayer(t *testing.T) {
	dir := writeChain(t, map[string]string{
		"policy/base.yaml": strictBase,
		FileName:           "extends: policy/base.yaml\ntimeouts: { review: 3m }\n",
	})
	for _, got := range []func() (domain.Config, error){
		NewRepository(dir).Load,
		func() (domain.Config, error) {
			_, _, r, err := loadBoth(t, dir)
			return r, err
		},
	} {
		cfg, err := got()
		if err != nil {
			t.Fatalf("a compliant strict chain must load: %v", err)
		}
		if cfg.Profile != "strict@1" {
			t.Errorf("effective profile = %q", cfg.Profile)
		}
		if !cfg.Signing.Required || cfg.Execution.Network != "none" {
			t.Errorf("the profile's defaults must apply under the chain: %+v %+v", cfg.Signing, cfg.Execution)
		}
		if cfg.Timeouts["review"] != "3m" || cfg.Timeouts["document"] != domain.DefaultAgentTimeout {
			t.Errorf("the chain overrides the base layer, the base fills gaps: %v", cfg.Timeouts)
		}
	}
}

func TestLoad_ChildCannotLowerInheritedProfile(t *testing.T) {
	dir := writeChain(t, map[string]string{
		"org/base.yaml": strictBase,
		FileName:        "extends: org/base.yaml\nprofile: standard\n",
	})
	_, wtErr, _, refErr := loadBoth(t, dir)
	for name, err := range map[string]error{"Load": wtErr, "ResolveAtRef": refErr} {
		if err == nil {
			t.Fatalf("%s: a child naming a lower profile must fail to load", name)
		}
		msg := err.Error()
		for _, want := range []string{"standard", "strict", ".warden.yaml", "org/base.yaml", "cannot lower"} {
			if !strings.Contains(msg, want) {
				t.Errorf("%s: error must name %q: %s", name, want, msg)
			}
		}
	}
}

func TestLoad_ChildMayRaiseOrRepeatProfile(t *testing.T) {
	for _, child := range []string{"profile: strict\n", "profile: strict@1\n", ""} {
		dir := writeChain(t, map[string]string{
			"org/base.yaml": strings.Replace(strictBase, "profile: strict", "profile: standard", 1),
			FileName:        "extends: org/base.yaml\n" + child,
		})
		cfg, err := NewRepository(dir).Load()
		if err != nil {
			t.Fatalf("child %q: %v", child, err)
		}
		want := "strict@1"
		if child == "" {
			want = "standard@1"
		}
		if cfg.Profile != want {
			t.Errorf("child %q: profile = %q, want %q", child, cfg.Profile, want)
		}
	}
}

// The lowering may sit two files away: a middle layer inherits strict from the
// root base and the repo file names permissive.
func TestLoad_LoweringIsCaughtAcrossAChain(t *testing.T) {
	dir := writeChain(t, map[string]string{
		"org/root.yaml": strictBase,
		"org/team.yaml": "extends: root.yaml\nnotify: false\n",
		FileName:        "extends: org/team.yaml\nprofile: permissive\n",
	})
	_, err := NewRepository(dir).Load()
	if err == nil || !strings.Contains(err.Error(), "org/root.yaml") || !strings.Contains(err.Error(), ".warden.yaml") {
		t.Fatalf("the error must name the repo file and the file that set strict: %v", err)
	}
}

// Floors are checked against the merged result, so it does not matter which
// file introduces the weakening — the error names whichever one did.
func TestLoad_FloorsCatchWeakeningHoweverIntroduced(t *testing.T) {
	cases := []struct {
		name        string
		child       string
		field, file string
	}{
		{"child sets host execution", "execution: { mode: host }\n", "execution.mode", ".warden.yaml"},
		{"child opens the network", "execution: { network: bridge }\n", "execution.network", ".warden.yaml"},
		{"child skips a required step", "rules:\n  - match: { branch: \"wip/*\" }\n    then: { steps: { pre_push: { skip: [security-scan] } } }\n",
			"rules[0].then.steps.pre_push.skip", ".warden.yaml"},
		{"child lets review auto-fix", "rules:\n  - then: { auto_fix: { review: 2 } }\n", "rules[0].then.auto_fix.review", ".warden.yaml"},
		{"child unbounds the agent", "timeouts: { review: \"0\" }\n", "timeouts.review", ".warden.yaml"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeChain(t, map[string]string{
				"policy/base.yaml": strictBase,
				FileName:           "extends: policy/base.yaml\n" + tc.child,
			})
			_, wtErr, _, refErr := loadBoth(t, dir)
			for name, err := range map[string]error{"Load": wtErr, "ResolveAtRef": refErr} {
				if err == nil {
					t.Fatalf("%s: the floor must refuse the config", name)
				}
				msg := err.Error()
				if !strings.Contains(msg, tc.field) || !strings.Contains(msg, "(set in "+tc.file+")") || !strings.Contains(msg, "strict@1") {
					t.Errorf("%s: error must name %s and %s: %s", name, tc.field, tc.file, msg)
				}
			}
		})
	}
	// The weakening can come from the base instead, under a child that names
	// the profile: the error then names the base.
	dir := writeChain(t, map[string]string{
		"policy/base.yaml": strings.NewReplacer("profile: strict\n", "", "mode: container", "mode: host").Replace(strictBase),
		FileName:           "extends: policy/base.yaml\nprofile: strict\n",
	})
	_, err := NewRepository(dir).Load()
	if err == nil || !strings.Contains(err.Error(), "execution.mode") || !strings.Contains(err.Error(), "(set in policy/base.yaml)") {
		t.Fatalf("a base that sets host under a strict child must be named: %v", err)
	}
}

func TestLoad_FloorErrorListsEveryViolation(t *testing.T) {
	dir := writeChain(t, map[string]string{FileName: "profile: strict\nexecution: { mode: host, network: bridge }\n"})
	_, err := NewRepository(dir).Load()
	if err == nil {
		t.Fatal("expected floor violations")
	}
	for _, want := range []string{"steps.pre_commit", "commands.lint", "commands.security-scan", "execution.mode", "execution.network"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must list %s: %s", want, err)
		}
	}
}

func TestLoad_UnknownProfileIsALoadError(t *testing.T) {
	for _, v := range []string{"paranoid", "strict@3", "Strict"} {
		dir := writeChain(t, map[string]string{FileName: "profile: " + v + "\n"})
		if _, err := NewRepository(dir).Load(); err == nil {
			t.Errorf("profile %q must fail the load", v)
		}
	}
	// In a base too, named by path.
	dir := writeChain(t, map[string]string{"b.yaml": "profile: strict@9\n", FileName: "extends: b.yaml\n"})
	if _, err := NewRepository(dir).Load(); err == nil || !strings.Contains(err.Error(), "b.yaml") {
		t.Errorf("an unknown profile in a base must name the base: %v", err)
	}
}

func TestLoad_PermissiveHasNoFloors(t *testing.T) {
	dir := writeChain(t, map[string]string{FileName: "profile: permissive\nexecution: { mode: host }\nagent: claude\n"})
	cfg, err := NewRepository(dir).Load()
	if err != nil {
		t.Fatalf("permissive@1 enforces nothing at load: %v", err)
	}
	if cfg.Signing.Required {
		t.Error("permissive@1 does not require signing")
	}
	// But it is a profile, so the bundled-preset agent applies.
	res := policy.Resolve(cfg, policy.Input{Hook: domain.PrePush, Branch: "main"})
	if res.AgentFor(domain.StepReview) != "claude" || res.Profile == nil || res.Profile.Name != "permissive@1" {
		t.Errorf("agent: claude must apply under a profile: agents=%v profile=%+v", res.Agents, res.Profile)
	}
}

func TestResolve_AgentAppliesOnlyForBundledPresets(t *testing.T) {
	cases := []struct {
		name, yaml string
		want       string
	}{
		{"bundled preset", "profile: permissive\nagent: codex\n", "codex"},
		{"auto stays inert", "profile: permissive\nagent: auto\n", ""},
		{"custom agent stays inert", "profile: permissive\nagent: mybot\nagent_commands: { mybot: \"mybot {prompt}\" }\n", ""},
		{"no profile stays inert", "agent: claude\n", ""},
		{"a rule still wins", "profile: permissive\nagent: claude\nrules:\n  - then: { agent: { review: codex } }\n", "codex"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := NewRepository(writeChain(t, map[string]string{FileName: tc.yaml})).Load()
			if err != nil {
				t.Fatal(err)
			}
			res := policy.Resolve(cfg, policy.Input{Hook: domain.PrePush, Branch: "main"})
			if got := res.AgentFor(domain.StepReview); got != tc.want {
				t.Errorf("review agent = %q, want %q", got, tc.want)
			}
			if res.AgentFor(domain.StepLint) != "" {
				t.Error("agent: applies only to the built-in agent steps")
			}
		})
	}
}

func TestResolve_ProfileInExplain(t *testing.T) {
	dir := writeChain(t, map[string]string{FileName: strictBase})
	cfg, err := NewRepository(dir).Load()
	if err != nil {
		t.Fatal(err)
	}
	res := policy.Resolve(cfg, policy.Input{Hook: domain.PrePush, Branch: "main"})
	p := res.Profile
	if p == nil || p.Name != "strict@1" || p.Level != "strict" {
		t.Fatalf("resolved profile = %+v", p)
	}
	strict, _ := domain.ParseComplianceProfile("strict")
	if p.Digest != strict.Digest() {
		t.Errorf("digest = %s, want %s", p.Digest, strict.Digest())
	}
	if len(p.Floors) != len(strict.Floors()) {
		t.Errorf("explain must list every floor: %d of %d", len(p.Floors), len(strict.Floors()))
	}
	for _, f := range p.Floors {
		if !f.Satisfied {
			t.Errorf("a config that loaded meets floor %s: %v", f.ID, f.Problems)
		}
	}
}

func TestParse_UnknownFieldHintsAtANewerRelease(t *testing.T) {
	old := ReaderVersion
	t.Cleanup(func() { ReaderVersion = old })
	ReaderVersion = "0.35.0"
	_, err := Parse([]byte("future_key: true\n"))
	if err == nil || !strings.Contains(err.Error(), "future_key") || !strings.Contains(err.Error(), "newer warden than v0.35.0") {
		t.Errorf("an unknown key must hint at a newer release: %v", err)
	}
	ReaderVersion = ""
	if _, err := Parse([]byte("future_key: true\n")); err == nil || !strings.Contains(err.Error(), "newer warden than this one") {
		t.Errorf("without a version the hint still reads: %v", err)
	}
	// A type error that is not an unknown key gets no such hint.
	if _, err := Parse([]byte("notify: [1]\n")); err == nil || strings.Contains(err.Error(), "newer warden") {
		t.Errorf("only an unknown key earns the hint: %v", err)
	}
}

func TestLoadStrictV2AndInheritedOptOut(t *testing.T) {
	base := strings.Replace(strictBase, "profile: strict", "profile: strict@2", 1)
	base = strings.Replace(base, `image: "golang:1.26"`, `image: "golang:1.26", limits: {memory: 512MiB, cpus: "1.5", pids: 128}`, 1)
	for _, optOut := range []bool{false, true} {
		child := "extends: base.yaml\n"
		if optOut {
			child += "execution: {attest_observations: false}\n"
		}
		dir := writeChain(t, map[string]string{"base.yaml": base, FileName: child})
		a, ae, b, be := loadBoth(t, dir)
		for _, result := range []struct {
			cfg domain.Config
			err error
		}{{a, ae}, {b, be}} {
			if optOut {
				if result.err == nil || !strings.Contains(result.err.Error(), "execution.attest_observations") {
					t.Fatalf("opt-out accepted: %v", result.err)
				}
				continue
			}
			if result.err != nil {
				t.Fatal(result.err)
			}
			if result.cfg.Profile != "strict@2" || result.cfg.Execution.AttestObservations == nil || !*result.cfg.Execution.AttestObservations {
				t.Fatalf("lost strict@2: %+v", result.cfg)
			}
		}
	}
}
