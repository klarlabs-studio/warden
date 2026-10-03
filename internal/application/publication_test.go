package application

import (
	"context"
	"errors"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

func TestRunner_PublicationStateMatchesArtifacts(t *testing.T) {
	for _, tc := range []struct {
		name               string
		writeErr, pushErr  error
		want               string
		written, published bool
	}{
		{"published", nil, nil, "published", true, true},
		{"write failure", errors.New("no identity"), nil, "missing", false, false},
		{"push failure", nil, errors.New("remote unavailable"), "local", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, mode := range []string{"delegated", "pushed", "attest-only"} {
				t.Run(mode, func(t *testing.T) {
					git := &fakeGit{root: t.TempDir(), branch: "feature", head: "sha1",
						wt:           &fakeWorktree{dir: "/wt", headSHA: "sha1"},
						writeNoteErr: tc.writeErr, pushNotesErr: tc.pushErr,
						rewritesHistory: mode == "pushed"}
					r := newRunner(t, git, &fakeKernel{outcomes: map[domain.StepName]domain.StepStatus{}}, fakeApprover{approve: true}, prePushCfg())
					r.Settings.AttestOnly = mode == "attest-only"
					res, err := r.Run(context.Background(), domain.PrePush)
					if mode == "attest-only" && tc.writeErr != nil {
						if res.Provenance != "missing" || res.PushPerformed || !res.AttestOnly {
							t.Fatalf("lost publication state on error: %+v", res)
						}
						if !errors.Is(err, ErrAttestationNotWritten) {
							t.Fatalf("error = %v", err)
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					if res.Outcome != domain.OutcomePassed || res.Provenance != tc.want {
						t.Fatalf("outcome=%s provenance=%s", res.Outcome, res.Provenance)
					}
					if git.wroteNote != tc.written || git.notesPushed != tc.published {
						t.Fatalf("artifacts: written=%t published=%t", git.wroteNote, git.notesPushed)
					}
					if res.PushPerformed != git.pushed {
						t.Fatalf("claimed push=%t actual=%t", res.PushPerformed, git.pushed)
					}
				})
			}
		})
	}
}

func TestRunner_UnpublishedProvenanceDoesNotOpenPRorPublishSuccess(t *testing.T) {
	git := &fakeGit{root: t.TempDir(), branch: "feature", head: "sha1",
		wt: &fakeWorktree{dir: "/wt", headSHA: "sha1"}, pushNotesErr: errors.New("remote rejected notes")}
	cfg := prePushCfg()
	cfg.PR = domain.PRConfig{Enabled: true}
	cfg.Status = domain.StatusConfig{Enabled: true}
	forge := &fakeForge{available: true}
	r := newRunner(t, git, &fakeKernel{outcomes: map[domain.StepName]domain.StepStatus{}}, fakeApprover{approve: true}, cfg)
	r.Forge = forge
	res, err := r.Run(context.Background(), domain.PrePush)
	if err != nil {
		t.Fatal(err)
	}
	if res.Provenance != "local" || !res.PushPerformed {
		t.Fatalf("unexpected result: %+v", res)
	}
	if forge.called || len(forge.statuses) != 0 {
		t.Fatal("unpublished evidence advertised as forge success")
	}
}
