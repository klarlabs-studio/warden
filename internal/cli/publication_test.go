package cli

import (
	"bytes"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

func TestPublicationFailureExitAndReporting(t *testing.T) {
	for _, state := range []string{"missing", "local"} {
		for _, mode := range []string{"delegated", "pushed", "attest-only"} {
			t.Run(state+"/"+mode, func(t *testing.T) {
				res := application.RunResult{Outcome: domain.OutcomePassed, Provenance: state,
					GitCompletesPush: mode == "delegated", AttestOnly: mode == "attest-only",
					PushPerformed: mode == "pushed"}
				var out bytes.Buffer
				if code := runPrePushExit(res, &out); code != exitProvenanceIncomplete {
					t.Fatalf("exit=%d", code)
				}
				if !strings.Contains(out.String(), "provenance="+state) {
					t.Fatal(out.String())
				}
				if mode != "pushed" && strings.Contains(out.String(), "already pushed") {
					t.Fatal("claimed a push", out.String())
				}
			})
		}
	}
}

func TestAttestOnlyDoesNotClaimGitPush(t *testing.T) {
	var out bytes.Buffer
	noteGitPushError(&out, application.RunResult{Outcome: domain.OutcomePassed, AttestOnly: true})
	if out.Len() != 0 {
		t.Fatal(out.String())
	}
}
