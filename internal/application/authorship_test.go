package application

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

const claudeCoAuthor = "Claude Opus 5 <noreply@anthropic.com>"

// authorshipRun is a pre-push run of sha1 under profile, with the trailers the
// final span read returns and an Agent Trace in the worktree when withTrace.
func authorshipRun(t *testing.T, profile string, trailers []domain.CommitTrailers, environ []string, withTrace bool) (*Runner, *fakeGit) {
	t.Helper()
	dir := t.TempDir()
	cfg := observedRunCfg(profile)
	if withTrace {
		cfg.AgentTrace.Path = ".agent-trace/trace.json"
		if err := os.MkdirAll(filepath.Join(dir, ".agent-trace"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".agent-trace", "trace.json"), []byte(`{"version":"0.1","id":"trace-1","files":[]}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git := &fakeGit{
		root: t.TempDir(), branch: "main", head: "sha1", spanBase: "base0",
		wt:           &fakeWorktree{dir: dir, headSHA: "sha1"},
		spanTrailers: map[string][]domain.CommitTrailers{"base0..sha1": trailers},
	}
	r := newRunner(t, git, observedRunKernel(), fakeApprover{approve: true}, cfg)
	r.Signer = newFakeSigner(t)
	r.Environ = func() []string { return environ }
	return r, git
}

func agentCommit() []domain.CommitTrailers {
	return []domain.CommitTrailers{
		{Commit: "sha1", Trailers: []domain.Trailer{{Key: "Co-Authored-By", Value: claudeCoAuthor}}},
		{Commit: "sha0"},
	}
}

// strict@1: a commit that declares an agent and no Agent Trace is refused
// before any step runs, and nothing is pushed or noted.
func TestRunner_StrictRefusesDeclaredAgentWithoutTrace(t *testing.T) {
	for name, tc := range map[string]struct {
		trailers []domain.CommitTrailers
		environ  []string
		want     string
	}{
		"trailer":    {agentCommit(), nil, `commit sha1 carries "Co-Authored-By: ` + claudeCoAuthor + `"`},
		"env marker": {nil, []string{"CLAUDE_CODE_CHILD_SESSION=1"}, "the gate runs under CLAUDE_CODE_CHILD_SESSION (claude-code)"},
	} {
		t.Run(name, func(t *testing.T) {
			r, git := authorshipRun(t, "strict", tc.trailers, tc.environ, false)
			r.Kernels.(*fakeFactory).kernel.execErr = errors.New("a step ran")
			_, err := r.Run(context.Background(), domain.PrePush)
			if !errors.Is(err, ErrAgentTraceRequired) {
				t.Fatalf("want ErrAgentTraceRequired, got %v", err)
			}
			for _, want := range []string{"strict@1", tc.want, "agent_trace.path"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal must say %q: %v", want, err)
				}
			}
			if git.pushed || git.wroteNote {
				t.Fatalf("a refused run must not push (%v) or write a note (%v)", git.pushed, git.wroteNote)
			}
		})
	}
}

// With the trace present, strict passes and the note binds both the trace and
// the declarations inside the signature.
func TestRunner_StrictWithTraceBindsTheDeclarations(t *testing.T) {
	r, git := authorshipRun(t, "strict", agentCommit(), []string{"GEMINI_CLI=1", "ANTHROPIC_API_KEY=sk-x"}, true)
	if _, err := r.Run(context.Background(), domain.PrePush); err != nil {
		t.Fatal(err)
	}
	rec := git.note
	if rec.AgentTrace == nil {
		t.Fatal("the trace must be notarized")
	}
	want := &domain.AuthorshipSignals{
		Trailers:   []domain.TrailerDeclaration{{Commit: "sha1", Agent: "claude-code", Trailer: "Co-Authored-By: " + claudeCoAuthor}},
		EnvMarkers: []domain.EnvMarkerDeclaration{{Name: "GEMINI_CLI", Agent: "gemini-cli"}},
	}
	if rec.Authorship == nil || !equalSignals(rec.Authorship, want) {
		t.Fatalf("authorship = %+v, want %+v", rec.Authorship, want)
	}
	if !rec.VerifySignature() {
		t.Fatal("the note must verify")
	}
	// The record's own span was read: (CoversFrom, CommitSHA].
	if last := git.spanTrailerReads[len(git.spanTrailerReads)-1]; last != "base0..sha1" || rec.CoversFrom != "base0" {
		t.Errorf("read %v for a record covering %q..%q", git.spanTrailerReads, rec.CoversFrom, rec.CommitSHA)
	}
	payload, _ := rec.SigningPayload()
	if strings.Contains(string(payload), "sk-x") || strings.Contains(string(payload), "ANTHROPIC") {
		t.Errorf("environment values leaked into the record: %s", payload)
	}
	tampered := rec
	tampered.Authorship = nil
	if tampered.VerifySignature() {
		t.Error("stripping the declarations must break the signature")
	}
}

// Below strict the declarations are recorded and require nothing.
func TestRunner_PermissiveRecordsDeclarationsWithoutRequiringATrace(t *testing.T) {
	r, git := authorshipRun(t, "permissive", agentCommit(), nil, false)
	if _, err := r.Run(context.Background(), domain.PrePush); err != nil {
		t.Fatalf("permissive must not require a trace: %v", err)
	}
	if a := git.note.Authorship; a == nil || len(a.Trailers) != 1 || a.Trailers[0].Commit != "sha1" {
		t.Fatalf("authorship = %+v, want the sha1 trailer", a)
	}
}

// Nothing declared: strict needs no trace, and the profiled note has no
// authorship field rather than one that could read as "a person wrote this".
func TestRunner_NoDeclarationRequiresNothingAndRecordsNothing(t *testing.T) {
	r, git := authorshipRun(t, "strict", []domain.CommitTrailers{{Commit: "sha1", Trailers: []domain.Trailer{{Key: "Signed-off-by", Value: "x"}}}}, []string{"CLAUDECODE=1"}, false)
	if _, err := r.Run(context.Background(), domain.PrePush); err != nil {
		t.Fatal(err)
	}
	if git.note.Authorship != nil || git.note.Profile == nil {
		t.Fatalf("want a profiled note with no authorship, got %+v / %+v", git.note.Authorship, git.note.Profile)
	}
}

// Without a profile nothing is read and nothing is recorded, so the note is
// byte-identical to before.
func TestRunner_UnprofiledRunReadsNoAuthorship(t *testing.T) {
	r, git := authorshipRun(t, "", agentCommit(), []string{"CLAUDE_CODE_CHILD_SESSION=1"}, false)
	if _, err := r.Run(context.Background(), domain.PrePush); err != nil {
		t.Fatal(err)
	}
	if git.note.Authorship != nil || len(git.spanTrailerReads) != 0 {
		t.Fatalf("an unprofiled run read %v and recorded %+v", git.spanTrailerReads, git.note.Authorship)
	}
}

// A span that cannot be read fails a profiled run: the record must not imply
// warden looked and found nothing.
func TestRunner_UnreadableSpanFailsAProfiledRun(t *testing.T) {
	r, git := authorshipRun(t, "permissive", nil, nil, false)
	git.spanTrailersErr = errors.New("git log exploded")
	_, err := r.Run(context.Background(), domain.PrePush)
	if err == nil || !strings.Contains(err.Error(), "git log exploded") {
		t.Fatalf("want the read error, got %v", err)
	}
	if git.pushed || git.wroteNote {
		t.Fatal("nothing may be published")
	}
}

// The final check stands on its own: it is decided on the attested span, not
// on what the preflight saw.
func TestBindAuthorship_RequiresTraceOnTheAttestedSpan(t *testing.T) {
	git := &fakeGit{spanTrailers: map[string][]domain.CommitTrailers{"base0..sha2": agentCommit()}}
	r := &Runner{Git: git, Environ: func() []string { return nil }}
	strict := domain.ResolvedPolicy{Profile: &domain.ResolvedProfile{Name: "strict@1"}}
	rec := &domain.RunRecord{CommitSHA: "sha2", CoversFrom: "base0"}
	if err := r.bindAuthorship(rec, strict, domain.Config{}); !errors.Is(err, ErrAgentTraceRequired) {
		t.Fatalf("want ErrAgentTraceRequired, got %v", err)
	}
	if rec.Authorship == nil {
		t.Fatal("the declarations are recorded even when the run is refused")
	}
	rec = &domain.RunRecord{CommitSHA: "sha2", CoversFrom: "base0", AgentTrace: &domain.AgentTraceRef{Digest: "d"}}
	if err := r.bindAuthorship(rec, strict, domain.Config{}); err != nil {
		t.Fatalf("a notarized trace satisfies strict: %v", err)
	}
	// An unbound record has nothing to bind declarations to.
	rec = &domain.RunRecord{}
	if err := r.bindAuthorship(rec, strict, domain.Config{}); err != nil || rec.Authorship != nil {
		t.Fatalf("unbound record: %v %+v", err, rec.Authorship)
	}
}

func equalSignals(a, b *domain.AuthorshipSignals) bool {
	if len(a.Trailers) != len(b.Trailers) || len(a.EnvMarkers) != len(b.EnvMarkers) {
		return false
	}
	for i := range a.Trailers {
		if a.Trailers[i] != b.Trailers[i] {
			return false
		}
	}
	for i := range a.EnvMarkers {
		if a.EnvMarkers[i] != b.EnvMarkers[i] {
			return false
		}
	}
	return true
}
