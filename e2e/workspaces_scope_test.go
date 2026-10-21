package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// Affected-scope provenance, the writer side (design 0006 §4), golden-fleet
// style. The fixture's shape (execFixture: utils <- ui <- web, api, a weird
// directory, lib <- svc) fixes the right answer independently of warden, and
// every scenario asserts three artifacts:
//
//  1. the workspaces the per-workspace command ACTUALLY ran in (its log);
//  2. what the signed note says it ran (note.scope.steps.test.selected);
//  3. what a verifier does with that note (exit code and reason).

// scopeRepo is an execRepo whose runs all sign with one stable key, so a
// full-scope note written by one run can anchor the next: e2e's default gives
// every invocation a fresh config directory, and so a fresh key.
type scopeRepo struct {
	*execRepo
	cfgDir  string
	fixture string // the fixture commit, on origin with no note
}

func newScopeRepo(t *testing.T) *scopeRepo {
	t.Helper()
	r := newExecRepo(t, execConfig)
	return &scopeRepo{execRepo: r, cfgDir: t.TempDir(), fixture: r.revParse("HEAD")}
}

// newAnchoredRepo gates the fixture commit itself: no ancestor has a note, so
// that run is full, and its signed full-scope note anchors what follows.
func newAnchoredRepo(t *testing.T) *scopeRepo {
	t.Helper()
	s := newScopeRepo(t)
	ran, rec, out := s.gate()
	if !reflect.DeepEqual(ran, allExecWorkspaces) || rec.Scope == nil || rec.Scope.Full == nil || rec.Scope.Full.Rule != domain.FullRuleNoAnchor {
		t.Fatalf("the first gate must be full (no anchor): ran %v scope %+v\n%s", ran, rec.Scope, out)
	}
	return s
}

func (s *scopeRepo) env(extra ...string) []string {
	return append([]string{"LOG=" + s.log, "WARDEN_CONFIG_DIR=" + s.cfgDir}, extra...)
}

// ranLog reads the workspaces the per-workspace command logged, sorted.
func (s *scopeRepo) ranLog() []string {
	s.t.Helper()
	data, err := os.ReadFile(s.log)
	if err != nil && !os.IsNotExist(err) {
		s.t.Fatal(err)
	}
	ran := []string{}
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if l != "" {
			ran = append(ran, l)
		}
	}
	sort.Strings(ran)
	return ran
}

// gateEnv runs the pre-push gate (plus args) with extra environment, then
// publishes the branch the way git would after a hook that left the push to
// it. It returns the log, the note on HEAD and the output.
func (s *scopeRepo) gateEnv(extra []string, args ...string) ([]string, domain.RunRecord, string) {
	s.t.Helper()
	_ = os.Remove(s.log)
	out, code := s.wardenEnv(s.env(extra...), append([]string{"run", "pre-push"}, args...)...)
	if code != 0 && code != 3 {
		s.t.Fatalf("pre-push exited %d:\n%s", code, out)
	}
	ran := s.ranLog()
	rec := s.note("HEAD")
	s.git("push", "-q", "--no-verify", "origin", "main")
	return ran, rec, out
}

func (s *scopeRepo) gate(args ...string) ([]string, domain.RunRecord, string) {
	s.t.Helper()
	return s.gateEnv(nil, args...)
}

func (s *scopeRepo) note(rev string) domain.RunRecord {
	s.t.Helper()
	data, err := exec.Command("git", "-C", s.dir, "notes", "--ref=warden", "show", rev).Output()
	if err != nil {
		s.t.Fatalf("no note on %s: %v", rev, err)
	}
	var rec domain.RunRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		s.t.Fatalf("decode note: %v\n%s", err, data)
	}
	return rec
}

func (s *scopeRepo) fingerprint() string {
	s.t.Helper()
	out, code := s.wardenEnv(s.env(), "key", "show")
	if code != 0 {
		s.t.Fatalf("key show: %d %s", code, out)
	}
	m := regexp.MustCompile(`fingerprint:\s*([0-9a-f]+)`).FindStringSubmatch(out)
	if m == nil {
		s.t.Fatalf("no fingerprint in: %s", out)
	}
	return m[1]
}

func (s *scopeRepo) change(files map[string]string, msg string) {
	s.t.Helper()
	s.writeFiles(files)
	s.git("commit", "--no-verify", "-q", "-m", msg)
}

// verify runs `warden verify` and returns its output and exit code.
func (s *scopeRepo) verify(args ...string) (string, int) {
	s.t.Helper()
	return s.wardenEnv(s.env(), append([]string{"verify"}, args...)...)
}

// testSelected is note.scope.steps.test.selected: the workspaces the note
// says the fixture's one per-workspace step ran for.
func testSelected(rec domain.RunRecord) []string {
	if rec.Scope == nil {
		return nil
	}
	return rec.Scope.Steps["test"].Selected
}

var leafChange = map[string]string{"packages/ui/index.ts": "export const ui = 2;\n"}

// A leaf's change runs it and its dependent, the note says exactly that, and
// every verifier refuses it as the partial check it is.
func TestGoldenFleet_ScopeAffectedLeafChange(t *testing.T) {
	s := newAnchoredRepo(t)
	s.change(leafChange, "leaf change")
	ran, rec, out := s.gate()
	want := []string{"apps/web", "packages/ui"}
	if !reflect.DeepEqual(ran, want) {
		t.Fatalf("ran %v, want %v\n%s", ran, want, out)
	}
	if rec.Scope == nil || rec.Scope.Kind != domain.SelectionAffected || rec.Scope.Anchor != s.fixture ||
		rec.Scope.HeadTree != s.revParse("HEAD^{tree}") || !reflect.DeepEqual(testSelected(rec), want) {
		t.Fatalf("scope %+v", rec.Scope)
	}
	if !strings.Contains(out, "scope affected: test[2/7 workspaces]") {
		t.Errorf("the run must say its scope:\n%s", out)
	}
	fp := s.fingerprint()
	if out, code := s.verify("--key", fp, "--commit", "HEAD"); code != 1 || !strings.Contains(out, "affected-scope") {
		t.Fatalf("verify --key: %d, want 1 with affected-scope:\n%s", code, out)
	}
	if out, code := s.verify("--range", s.fixture+"..HEAD", "--key", fp, "--json"); code != 1 || !strings.Contains(out, `"reason": "affected-scope"`) {
		t.Fatalf("verify --range: %d, want 1 with affected-scope:\n%s", code, out)
	}
	// Well-formed requirements; the record cannot meet them whatever they
	// say, because version 1 requirements describe a full run.
	digest := "sha256:" + strings.Repeat("a", 64)
	reqs, err := json.Marshal(domain.ExecutionRequirements{Version: 1, Steps: map[domain.StepName]domain.StepRequirements{
		"test": {Command: "true", ConfigurationDigest: digest, ContextDigest: digest, ImageID: digest, ImageIDKind: "config",
			Platform: "linux/amd64", Network: "none", Inputs: map[string]string{}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	reqFile := filepath.Join(t.TempDir(), "requirements.json")
	if err := os.WriteFile(reqFile, reqs, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, code := s.verify("--key", fp, "--requirements", reqFile); code != 1 || !strings.Contains(out, "affected-scope") {
		t.Fatalf("verify --requirements: %d, want 1 naming the affected scope:\n%s", code, out)
	}

	// The anchor stays the full run: an affected note never anchors, so the
	// next change is measured from the fixture again, covering both changes.
	s.change(map[string]string{"apps/api/index.ts": "export {};\n"}, "api change")
	ran, rec, out = s.gate()
	want = []string{"apps/api", "apps/web", "packages/ui"}
	if !reflect.DeepEqual(ran, want) || rec.Scope.Anchor != s.fixture || !reflect.DeepEqual(testSelected(rec), want) {
		t.Fatalf("second affected run: ran %v, scope %+v\n%s", ran, rec.Scope, out)
	}
}

// Rules that force full run everything, the note says full and why, and a
// full-scope note verifies as before.
func TestGoldenFleet_ScopeForcedFull(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change map[string]string
		rule   domain.FullRule
	}{
		{"LockfileForcesFull", map[string]string{"pnpm-lock.yaml": "lockfileVersion: '9.1'\n"}, domain.FullRuleLockfile},
		{"UnownedPathForcesFull", map[string]string{"README.md": "changed\n"}, domain.FullRuleUnowned},
		// A comment leaves the configuration digest unchanged, so the anchor
		// still qualifies; the changed config file itself forces full.
		{"ConfigChangeForcesFull", map[string]string{".warden.yaml": execConfig + "# edited\n"}, domain.FullRuleConfiguration},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newAnchoredRepo(t)
			s.change(tc.change, tc.name)
			ran, rec, out := s.gate()
			if !reflect.DeepEqual(ran, allExecWorkspaces) || !reflect.DeepEqual(testSelected(rec), allExecWorkspaces) {
				t.Fatalf("ran %v, scope %+v\n%s", ran, rec.Scope, out)
			}
			if !rec.Scope.IsFull() || rec.Scope.Full == nil || rec.Scope.Full.Rule != tc.rule {
				t.Fatalf("want full (%s), got %+v", tc.rule, rec.Scope)
			}
			if out, code := s.verify("--key", s.fingerprint(), "--commit", "HEAD"); code != 0 {
				t.Fatalf("a full-scope note must verify: %d\n%s", code, out)
			}
		})
	}
}

// A rename is a delete plus an add: both workspaces, and the dependents of
// the one it left, are selected.
func TestGoldenFleet_ScopeRenameAcrossWorkspaces(t *testing.T) {
	s := newAnchoredRepo(t)
	s.git("mv", "packages/ui/index.ts", "apps/api/index.ts")
	s.git("commit", "--no-verify", "-q", "-m", "move")
	ran, rec, out := s.gate()
	want := []string{"apps/api", "apps/web", "packages/ui"}
	if !reflect.DeepEqual(ran, want) || rec.Scope.Kind != domain.SelectionAffected || !reflect.DeepEqual(testSelected(rec), want) {
		t.Fatalf("ran %v, scope %+v\n%s", ran, rec.Scope, out)
	}
}

// Deleting a workspace still reaches what depended on it, through the
// anchor's graph.
func TestGoldenFleet_ScopeDeletedWorkspaceReachesDependents(t *testing.T) {
	s := newAnchoredRepo(t)
	s.git("rm", "-q", "-r", "packages/utils")
	s.git("commit", "--no-verify", "-q", "-m", "drop utils")
	ran, rec, out := s.gate()
	want := []string{"apps/web", "packages/ui"}
	if !reflect.DeepEqual(ran, want) || rec.Scope.Kind != domain.SelectionAffected || !reflect.DeepEqual(testSelected(rec), want) ||
		!reflect.DeepEqual(rec.Scope.Removed, []string{"packages/utils"}) {
		t.Fatalf("ran %v, scope %+v\n%s", ran, rec.Scope, out)
	}
}

// A hand-edited note cannot shed or relabel its scope: the signature covers
// it, so the pinned verify refuses the edit as tampering, and so does an
// unpinned one.
func TestGoldenFleet_ScopeCannotBeStripped(t *testing.T) {
	s := newAnchoredRepo(t)
	s.change(leafChange, "leaf change")
	if _, rec, _ := s.gate(); rec.Scope == nil || rec.Scope.Kind != domain.SelectionAffected {
		t.Fatalf("setup: scope %+v", rec.Scope)
	}
	raw, err := exec.Command("git", "-C", s.dir, "notes", "--ref=warden", "show", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	fp := s.fingerprint()
	for name, edit := range map[string]func(map[string]any){
		"scope removed": func(m map[string]any) { delete(m, "scope") },
		"relabelled full": func(m map[string]any) {
			scope := m["scope"].(map[string]any)
			scope["kind"] = "full"
			delete(scope, "anchor")
			delete(scope, "anchor_tree")
			scope["full"] = map[string]any{"rule": "lockfile"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatal(err)
			}
			edit(m)
			edited, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			s.git("notes", "--ref=warden", "add", "-f", "-m", string(edited), "HEAD")
			if out, code := s.verify("--key", fp, "--commit", "HEAD"); code != 1 || strings.Contains(out, "affected-scope") {
				t.Fatalf("pinned verify of an edited note: %d, want 1 as a broken signature, not a scope refusal:\n%s", code, out)
			}
			if out, code := s.verify("--commit", "HEAD"); code != 1 {
				t.Fatalf("unpinned verify of an edited note: %d\n%s", code, out)
			}
		})
	}
}

// A squash-merge's re-attestation carries the original record byte for
// byte, scope included: the squash commit is as partial as what it squashed.
func TestGoldenFleet_ScopeSquashReattestKeepsScope(t *testing.T) {
	s := newAnchoredRepo(t)
	s.change(leafChange, "leaf change")
	if _, rec, _ := s.gate(); rec.Scope == nil || rec.Scope.Kind != domain.SelectionAffected {
		t.Fatalf("setup: scope %+v", rec.Scope)
	}
	gated := s.revParse("HEAD")
	cmd := exec.Command("git", "-C", s.dir, "commit-tree", gated+"^{tree}", "-p", s.fixture, "-m", "squash")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	squash := strings.TrimSpace(string(out))
	if out, code := s.wardenEnv(s.env(), "reattest", "--commit", squash); code != 0 {
		t.Fatalf("reattest: %d\n%s", code, out)
	}
	rec := s.note(squash)
	if rec.ReattestedFrom != gated || rec.FullScope() || rec.Scope == nil || rec.Scope.Kind != domain.SelectionAffected ||
		!reflect.DeepEqual(testSelected(rec), []string{"apps/web", "packages/ui"}) {
		t.Fatalf("re-attested note: from %s scope %+v", rec.ReattestedFrom, rec.Scope)
	}
	if out, code := s.verify("--key", s.fingerprint(), "--commit", squash); code != 1 || !strings.Contains(out, "affected-scope") {
		t.Fatalf("verify of the squash: %d, want 1 with affected-scope:\n%s", code, out)
	}
}

// The commits of a push span inherit their tip's scope: not verified by the
// range gate, and counted as gated by a partial check — neither verified
// nor bypassed — by doctor's bookkeeping.
func TestGoldenFleet_ScopeSpanUnderAffectedTip(t *testing.T) {
	s := newAnchoredRepo(t)
	s.change(leafChange, "ui change")
	mid := s.revParse("HEAD")
	s.change(map[string]string{"apps/web/index.ts": "export {};\n"}, "web change")
	ran, rec, out := s.gate()
	want := []string{"apps/web", "packages/ui"}
	if !reflect.DeepEqual(ran, want) || rec.Scope.Kind != domain.SelectionAffected || rec.CoversFrom != s.fixture {
		t.Fatalf("ran %v, scope %+v, covers from %s\n%s", ran, rec.Scope, rec.CoversFrom, out)
	}
	out, code := s.verify("--range", s.fixture+"..HEAD", "--key", s.fingerprint(), "--json")
	if code != 1 {
		t.Fatalf("verify --range: %d\n%s", code, out)
	}
	var rep struct {
		Commits []domain.CommitVerdict `json:"commits"`
	}
	if err := json.Unmarshal([]byte(out[strings.Index(out, "{"):]), &rep); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	for _, c := range rep.Commits {
		if c.Reason != domain.ReasonAffectedScope {
			t.Errorf("commit %s: reason %q, want affected-scope", c.SHA, c.Reason)
		}
		if c.SHA == mid && c.CoveredBy != s.revParse("HEAD") {
			t.Errorf("the span commit must name its covering note: %+v", c)
		}
	}
	got := s.classify()
	if got.Affected != 2 || got.Verified != 1 || got.Bypassed != 0 || got.Covered != 0 {
		t.Fatalf("fleet counts %+v: want 2 affected (tip and span), 1 verified (the anchor), 0 bypassed", got)
	}
	doctor, _ := s.wardenEnv(s.env(), "doctor")
	for _, want := range []string{"affected 2/7: partial check", "2 gated by an affected-scope run"} {
		if !strings.Contains(doctor, want) {
			t.Errorf("doctor output missing %q:\n%s", want, doctor)
		}
	}
}

// Each of these runs full, whatever an anchor would allow, and says why.
func TestGoldenFleet_ScopeRunsFull(t *testing.T) {
	t.Run("NoAnchorRunsFull", func(t *testing.T) {
		s := newScopeRepo(t)
		s.change(leafChange, "leaf change")
		ran, rec, _ := s.gate()
		if !reflect.DeepEqual(ran, allExecWorkspaces) || rec.Scope.Full == nil || rec.Scope.Full.Rule != domain.FullRuleNoAnchor || !rec.Signed() {
			t.Fatalf("ran %v scope %+v", ran, rec.Scope)
		}
		if out, code := s.verify("--key", s.fingerprint(), "--commit", "HEAD"); code != 0 {
			t.Fatalf("verify: %d\n%s", code, out)
		}
	})
	t.Run("NoSignerRunsFull", func(t *testing.T) {
		s := newAnchoredRepo(t)
		s.change(leafChange, "leaf change")
		// A config directory that is a file: no key can be created or read.
		blocked := filepath.Join(t.TempDir(), "not-a-dir")
		if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		ran, rec, out := s.gateEnv([]string{"WARDEN_CONFIG_DIR=" + blocked})
		if !reflect.DeepEqual(ran, allExecWorkspaces) || rec.Scope == nil || rec.Scope.Full == nil || rec.Scope.Full.Rule != domain.FullRuleNoSigner || rec.Signed() {
			t.Fatalf("ran %v scope %+v signed %v\n%s", ran, rec.Scope, rec.Signed(), out)
		}
	})
	t.Run("AttestOnlyIsFull", func(t *testing.T) {
		s := newAnchoredRepo(t)
		s.change(leafChange, "leaf change")
		s.git("push", "-q", "--no-verify", "origin", "main")
		ran, rec, _ := s.gate("--attest-only")
		if !reflect.DeepEqual(ran, allExecWorkspaces) || rec.Scope.Full == nil || rec.Scope.Full.Rule != domain.FullRuleAttestOnly || !rec.Signed() {
			t.Fatalf("ran %v scope %+v", ran, rec.Scope)
		}
		if out, code := s.verify("--key", s.fingerprint(), "--commit", "HEAD"); code != 0 {
			t.Fatalf("verify: %d\n%s", code, out)
		}
	})
}
