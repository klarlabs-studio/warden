package domain

import (
	"crypto/ed25519"
	"encoding/base64"
	"reflect"
	"strings"
	"testing"
	"time"
)

// A deadline is evidence only for a step that executed under it: a cached,
// predicted or skipped step ran nothing, so the deadline in force says nothing
// about it.
func TestStepDeadlinesCoverOnlyExecutedSteps(t *testing.T) {
	run := newTestRun()
	for _, res := range []StepResult{
		{Step: StepLint, Status: StepPass, Deadline: 5 * time.Minute},
		{Step: StepTest, Status: StepPass, Outcome: StepCached, Deadline: 5 * time.Minute},
		{Step: StepSecurityScan, Status: StepPass, Deadline: 10 * time.Minute, Reuse: &ExecutionReuse{Source: "cache"}},
		{Step: StepReview, Status: StepPass},
	} {
		if err := run.RecordStep(res); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := run.StepDeadlines(), map[StepName]int64{StepLint: 300000}; !reflect.DeepEqual(got, want) {
		t.Fatalf("deadlines = %v, want %v", got, want)
	}
}

func TestStepDeadlinesAreSigned(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	rec := RunRecord{CommitSHA: "tip", PublicKey: base64.StdEncoding.EncodeToString(pub), Deadlines: map[StepName]int64{StepLint: 300000}}
	payload, err := rec.SigningPayload()
	if err != nil {
		t.Fatal(err)
	}
	rec.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))
	if !rec.VerifySignature() {
		t.Fatal("a record with deadlines must verify")
	}

	tampered := rec
	tampered.Deadlines = map[StepName]int64{StepLint: 0}
	if tampered.VerifySignature() {
		t.Fatal("rewriting a deadline must break the signature")
	}
	tampered.Deadlines = nil
	if tampered.VerifySignature() {
		t.Fatal("stripping the deadlines must break the signature")
	}

	// Records written without the field keep their exact payload, so a note
	// that carries no deadlines still verifies under an older warden.
	legacy, _ := (RunRecord{}).SigningPayload()
	if strings.Contains(string(legacy), "deadlines") {
		t.Fatal("legacy payload changed")
	}
}
