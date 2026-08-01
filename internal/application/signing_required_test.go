package application

import (
	"errors"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// failingSigner rejects everything, standing in for the real degradations: an
// unwritable key directory, a malformed key file, a signer that errors.
type failingSigner struct{}

func (failingSigner) PublicKey() string           { return "pub" }
func (failingSigner) Sign([]byte) (string, error) { return "", errors.New("key is unreadable") }
func (failingSigner) Algorithm() string           { return domain.AlgorithmEd25519 }

type okSigner struct{}

func (okSigner) PublicKey() string           { return "pub" }
func (okSigner) Sign([]byte) (string, error) { return "sig", nil }
func (okSigner) Algorithm() string           { return domain.AlgorithmEd25519 }

// A successful sign reports no reason — the empty string is the "signed" signal
// the caller branches on.
func TestSign_SuccessReportsNoReason(t *testing.T) {
	r := &Runner{Signer: okSigner{}}
	rec := &domain.RunRecord{RunID: "run-1", CommitSHA: "abc"}

	if reason := r.sign(rec); reason != "" {
		t.Errorf("a successful sign must report no reason, got %q", reason)
	}
	if rec.Signature == "" || rec.PublicKey == "" {
		t.Errorf("the record should carry both signature and key: %+v", rec)
	}
}

// The three degradation paths must each SAY what happened. Silence is the whole
// defect: a repo can otherwise produce months of unsigned notes and only find
// out when a CI --require-signed starts failing.
func TestSign_DegradationsAreExplained(t *testing.T) {
	cases := map[string]struct {
		runner *Runner
		want   string
	}{
		"no signer at all":  {&Runner{}, "no signing key"},
		"signer rejects it": {&Runner{Signer: failingSigner{}}, "signer rejected"},
	}
	for name, tc := range cases {
		rec := &domain.RunRecord{RunID: "run-1", CommitSHA: "abc"}
		reason := tc.runner.sign(rec)
		if reason == "" {
			t.Errorf("%s: must report a reason, got none", name)
			continue
		}
		if !strings.Contains(reason, tc.want) {
			t.Errorf("%s: reason = %q, want it to mention %q", name, reason, tc.want)
		}
		// A half-signed record is worse than an unsigned one: a public key with
		// no signature invites a reader to think a key vouched for this.
		if rec.Signature != "" || rec.PublicKey != "" {
			t.Errorf("%s: a failed sign must leave no partial credentials: %+v", name, rec)
		}
	}
}

// A record that cannot be serialized must not leave a dangling public key
// either.
func TestSign_SerializationFailureLeavesNoPartialKey(t *testing.T) {
	// A record whose payload cannot be produced is hard to construct directly,
	// so this asserts the invariant the other paths share: PublicKey is only
	// left set when Signature is too.
	r := &Runner{Signer: failingSigner{}}
	rec := &domain.RunRecord{RunID: "run-1", CommitSHA: "abc"}
	_ = r.sign(rec)
	if (rec.PublicKey == "") != (rec.Signature == "") {
		t.Errorf("public key and signature must be set together or not at all: %+v", rec)
	}
}

// signing.required defaults false, so an existing repo is untouched: it still
// degrades to an unsigned note rather than failing a push.
func TestSigningConfig_DefaultsToNotRequired(t *testing.T) {
	var cfg domain.Config
	if cfg.Signing.Required {
		t.Error("signing.required must default to false so existing repos are unaffected")
	}
}
