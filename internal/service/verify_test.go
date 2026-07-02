package service

import (
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

func TestService_Verify(t *testing.T) {
	dir := initRepo(t)
	svc, err := New(dir, "test", autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	head, err := svc.Repo().HeadSHA()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("no note is unverified", func(t *testing.T) {
		res, err := svc.Verify("")
		if err != nil {
			t.Fatal(err)
		}
		if res.Validated || res.SHA != head {
			t.Errorf("expected unvalidated HEAD, got %+v", res)
		}
	})

	t.Run("intact note validates", func(t *testing.T) {
		rec := domain.RunRecord{
			RunID:             "run_x",
			StepsRun:          []domain.StepName{"lint", "test"},
			EvidenceChainRoot: "h0",
			Evidence: []domain.EvidenceEntry{
				{Hash: "h0"},
				{Hash: "h1", PreviousHash: "h0"},
			},
		}
		if err := svc.Repo().WriteNote(head, rec); err != nil {
			t.Fatal(err)
		}
		res, err := svc.Verify("")
		if err != nil {
			t.Fatal(err)
		}
		if !res.Validated || res.Record == nil || res.Record.RunID != "run_x" {
			t.Errorf("expected validated commit with record, got %+v", res)
		}
	})

	t.Run("tampered note fails", func(t *testing.T) {
		bad := domain.RunRecord{
			EvidenceChainRoot: "forged",
			Evidence:          []domain.EvidenceEntry{{Hash: "h0"}},
		}
		if err := svc.Repo().WriteNote(head, bad); err != nil {
			t.Fatal(err)
		}
		res, err := svc.Verify("")
		if err != nil {
			t.Fatal(err)
		}
		if res.Validated {
			t.Error("a note whose root does not match its chain must not validate")
		}
	})
}
