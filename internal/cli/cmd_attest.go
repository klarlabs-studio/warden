package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/service"
)

// cmdAttest handles `warden attest`, projecting a commit's refs/notes/warden
// record into an in-toto Statement so warden provenance can feed the wider
// supply-chain ecosystem (sigstore, GUAC, policy engines) instead of staying a
// warden-only note shape. It is a read-only projection — no signing, no writes;
// wrap the output in a DSSE envelope / cosign attest if you want it signed as a
// statement. Exits non-zero when the commit carries no note (nothing to attest);
// the trust of the underlying note is reported in the predicate's `verification`
// block, not the exit code (use `warden verify` to gate on trust).
func cmdAttest(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("attest", flag.ContinueOnError)
	fs.SetOutput(stderr)
	commit := fs.String("commit", "HEAD", "commit to attest")
	keys := fs.String("key", "", "trusted signer key(s)/fingerprint(s) used to set predicate.verification.trusted; falls back to .warden.yaml trusted_keys")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	svc, err := newService(autoApprover{})
	if err != nil {
		return fail(stderr, err)
	}
	trusted, _ := resolveTrustedKeys(svc, *keys)
	res, err := svc.Verify(*commit, trusted...)
	if err != nil {
		return fail(stderr, err)
	}
	if res.Record == nil {
		fmt.Fprintf(stderr, "warden: no provenance note on %s — nothing to attest\n", short(res.SHA))
		return 1
	}

	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(buildStatement(res)); err != nil {
		return fail(stderr, err)
	}
	return 0
}

// in-toto Statement (https://in-toto.io/Statement/v1) carrying a warden
// provenance predicate. A warden-specific predicateType is deliberate: warden
// attests *source* provenance (a commit was linted/tested/reviewed under
// policy), which is not SLSA *build* provenance — claiming slsa.dev/provenance
// would misrepresent it. Consumers key off predicateType and read the fields.
const (
	statementType     = "https://in-toto.io/Statement/v1"
	wardenPredicateID = "https://warden.klarlabs.de/provenance/v1"
)

type intotoStatement struct {
	Type          string          `json:"_type"`
	Subject       []intotoSubject `json:"subject"`
	PredicateType string          `json:"predicateType"`
	Predicate     attestPredicate `json:"predicate"`
}

type intotoSubject struct {
	Name   string            `json:"name"`
	Digest map[string]string `json:"digest"`
}

type attestPredicate struct {
	RunID         string                      `json:"runId"`
	Timestamp     string                      `json:"timestamp,omitempty"`
	WardenVersion string                      `json:"wardenVersion,omitempty"`
	StepsRun      []domain.StepName           `json:"stepsRun,omitempty"`
	MatchedRules  []string                    `json:"matchedRules,omitempty"`
	Evidence      *evidenceBlock              `json:"evidence,omitempty"`
	Dependencies  []domain.DependencyManifest `json:"dependencies,omitempty"`
	Signer        *signerBlock                `json:"signer,omitempty"`
	Verification  verificationBlock           `json:"verification"`
}

type evidenceBlock struct {
	ChainRoot string                 `json:"chainRoot"`
	Entries   []domain.EvidenceEntry `json:"entries"`
}

type signerBlock struct {
	Fingerprint string `json:"fingerprint"`
	PublicKey   string `json:"publicKey"`
}

type verificationBlock struct {
	// Attested is true when the note exists, its evidence chain is intact, and it
	// binds to this exact commit (RunRecord.Attests). SignatureValid whether the
	// signature verifies against its embedded key. Trusted whether that signer is
	// in the pinned/roster key set (only meaningful when one was supplied).
	Attested       bool `json:"attested"`
	SignatureValid bool `json:"signatureValid"`
	Trusted        bool `json:"trusted"`
}

// buildStatement projects a verified note into the in-toto Statement. It never
// invents data: every predicate field comes from the signed RunRecord or the
// verification result.
func buildStatement(res service.VerifyResult) intotoStatement {
	rec := res.Record
	pred := attestPredicate{
		RunID:         rec.RunID,
		Timestamp:     rec.Timestamp,
		WardenVersion: rec.WardenVersion,
		StepsRun:      rec.StepsRun,
		MatchedRules:  rec.MatchedRules,
		Dependencies:  rec.Dependencies,
		Verification: verificationBlock{
			Attested:       res.Validated || rec.Attests(res.SHA),
			SignatureValid: res.SignatureValid,
			Trusted:        res.Trusted,
		},
	}
	if len(rec.Evidence) > 0 {
		pred.Evidence = &evidenceBlock{ChainRoot: rec.EvidenceChainRoot, Entries: rec.Evidence}
	}
	if rec.Signed() {
		pred.Signer = &signerBlock{Fingerprint: rec.SignerFingerprint(), PublicKey: rec.PublicKey}
	}
	return intotoStatement{
		Type:          statementType,
		Subject:       []intotoSubject{{Name: "git+commit", Digest: map[string]string{"gitCommit": res.SHA}}},
		PredicateType: wardenPredicateID,
		Predicate:     pred,
	}
}
