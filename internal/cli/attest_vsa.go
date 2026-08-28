package cli

import (
	"strings"

	"go.klarlabs.de/warden/internal/service"
)

// The SLSA Verification Summary Attestation projection.
//
// WHY THIS EXISTS. Warden's note already says "a verifier ran a policy against
// this thing and here is the result" — which is precisely what a VSA says. SLSA
// defines VSA at the ARTIFACT layer (someone verified this tarball); warden's is
// the same statement one layer earlier, about the commit the artifact is built
// from. Emitting it in the vocabulary security teams and CI vendors already
// consume means they do not have to learn a warden-specific predicate to answer
// "was this checked, and by whom?".
//
// WHY IT DOES NOT REPLACE THE NATIVE PREDICATE. VSA is a summary: it has nowhere
// to put the hash-chained evidence entries or the dependency SBOM, which are the
// parts that make warden's note verifiable rather than merely assertive. So the
// warden predicate stays the default and the full record; VSA is the interop
// view, requested with `--predicate vsa`.
//
// WHAT IT DELIBERATELY DOES NOT CLAIM. The existing predicate carries a comment
// explaining that warden must not claim `slsa.dev/provenance`, because warden
// attests SOURCE provenance and that predicate means BUILD provenance. The same
// discipline applies inside a VSA, and it lands on `verifiedLevels`: warden does
// not produce a SLSA build level and must never say it did. SlsaResult is an
// open enum precisely so verifiers can report their own results, so warden
// reports warden's. A consumer looking for SLSA_BUILD_LEVEL_3 correctly finds
// nothing here.
//
// SLSA v1.2 added a SOURCE track, which is warden's actual subject matter and a
// different enum from the build one this paragraph was written about. Whether
// warden should claim SLSA_SOURCE_LEVEL_n — and the accepted consequence that a
// conformant consumer, required to ignore unrecognized levels, reads this
// statement as asserting none — is settled in docs/adr/0004. Short version:
// every source level is a property the SCS attests, and warden is not the SCS.
const vsaPredicateID = "https://slsa.dev/verification_summary/v1"

// Predicate selectors for `warden attest --predicate`.
const (
	predicateWarden = "warden"
	predicateVSA    = "vsa"
)

// verifierID identifies warden as the verifier that produced the summary. It is
// a TypeURI, not a fetchable endpoint.
const verifierID = "https://warden.klarlabs.de"

// Verified levels warden can honestly assert. They are namespaced so they can
// never be mistaken for a SLSA build level, and they are ordered by how much
// they actually prove:
//
//   - GATED     the configured policy ran and passed against this exact commit
//   - SIGNED    …and the note carries a signature that verifies
//   - TRUSTED   …and that signature was made by a key the caller pinned
//
// A verifier that only checked the chain must not be able to imply it checked
// the signer, so these accumulate rather than replace.
const (
	levelGated   = "WARDEN_SOURCE_GATED"
	levelSigned  = "WARDEN_SOURCE_SIGNED"
	levelTrusted = "WARDEN_SOURCE_TRUSTED"
)

// vsaStatement is an in-toto Statement carrying a SLSA VSA predicate.
type vsaStatement struct {
	Type          string          `json:"_type"`
	Subject       []intotoSubject `json:"subject"`
	PredicateType string          `json:"predicateType"`
	Predicate     vsaPredicate    `json:"predicate"`
}

// vsaPredicate is the SLSA VSA v1 predicate. Optional fields warden cannot fill
// honestly are omitted rather than guessed at — see buildVSA.
type vsaPredicate struct {
	Verifier           vsaVerifier   `json:"verifier"`
	TimeVerified       string        `json:"timeVerified,omitempty"`
	ResourceURI        string        `json:"resourceUri"`
	Policy             vsaDescriptor `json:"policy"`
	VerificationResult string        `json:"verificationResult"`
	VerifiedLevels     []string      `json:"verifiedLevels"`
}

type vsaVerifier struct {
	ID string `json:"id"`
	// Version names the warden that produced the note, read from the signed
	// record rather than from the binary running now — the summary describes the
	// verification that happened, not the one that could happen today.
	Version map[string]string `json:"version,omitempty"`
}

// vsaDescriptor is an in-toto ResourceDescriptor. Digest is omitted when warden
// has none: a ResourceDescriptor with no digest still identifies the resource,
// whereas a fabricated one would make the statement unverifiable in the worst
// way — by looking verifiable.
type vsaDescriptor struct {
	URI    string            `json:"uri"`
	Digest map[string]string `json:"digest,omitempty"`
}

// buildVSA projects a verified note into a SLSA VSA. Like buildStatement it
// invents nothing: every field is read from the signed RunRecord or the
// verification result, and anything warden does not have is left out.
//
// remoteURL names the repository when one is configured; it is only used to make
// the resource and policy URIs resolvable, and a repo without a remote still
// produces a valid statement identified by commit alone.
func buildVSA(res service.VerifyResult, remoteURL string, sourceRefs []string) vsaStatement {
	rec := res.Record
	attested := res.Validated || rec.Attests(res.SHA)

	// verificationResult is the gate's verdict, and it turns on the same
	// fail-closed question `warden verify` asks: does an intact note attest THIS
	// commit? A note that exists but binds elsewhere is FAILED, not PASSED.
	result := "FAILED"
	levels := []string{}
	if attested {
		result = "PASSED"
		levels = append(levels, levelGated)
		// Each further level requires the previous one: an unattested note's
		// signature proves the note was signed, not that the commit was gated.
		if res.SignatureValid {
			levels = append(levels, levelSigned)
			if res.Trusted {
				levels = append(levels, levelTrusted)
			}
		}
	}

	pred := vsaPredicate{
		Verifier:           vsaVerifier{ID: verifierID},
		TimeVerified:       rec.Timestamp,
		ResourceURI:        commitResourceURI(remoteURL, res.SHA),
		Policy:             vsaPolicy(remoteURL, res.SHA),
		VerificationResult: result,
		VerifiedLevels:     levels,
	}
	if rec.WardenVersion != "" {
		pred.Verifier.Version = map[string]string{"warden": rec.WardenVersion}
	}

	subject := intotoSubject{Name: "git+commit", Digest: map[string]string{"gitCommit": res.SHA}}
	// `source_refs` is a SHOULD in the VSA spec, and consumers are told to check
	// that an allowed branch appears in it — it is what stops a revision being
	// presented under a ref it was never on. Emit it only when refs actually
	// point at the commit; an empty list would assert "no refs" rather than
	// "warden did not look", and those are different claims.
	if len(sourceRefs) > 0 {
		subject.Annotations = map[string]any{"source_refs": sourceRefs}
	}

	return vsaStatement{
		Type:          statementType,
		Subject:       []intotoSubject{subject},
		PredicateType: vsaPredicateID,
		Predicate:     pred,
	}
}

// commitResourceURI names the verified resource: the commit itself.
//
// With a remote it uses the `git+<url>@<sha>` form pip and npm already use, so
// the identifier resolves to a real object someone else can fetch and check.
// Without one it falls back to `git:<sha>` — less useful, but honest: a
// local-only checkout genuinely has no globally resolvable name, and inventing a
// URL would be worse than admitting that.
func commitResourceURI(remoteURL, sha string) string {
	if remoteURL == "" {
		return "git:" + sha
	}
	return "git+" + normalizeRemote(remoteURL) + "@" + sha
}

// vsaPolicy points at the policy that was applied: the repository's .warden.yaml
// AS IT WAS at the verified commit, which the fragment pins exactly.
//
// The digest is omitted deliberately. A VSA's policy descriptor would ideally
// carry one, but the RunRecord does not record a hash of the config, so warden
// cannot supply it without either re-reading a file that may since have changed
// (which would describe a different policy than the one that ran) or fabricating
// a value. Naming the file at that commit is precise and checkable; a wrong
// digest would be neither.
func vsaPolicy(remoteURL, sha string) vsaDescriptor {
	return vsaDescriptor{URI: commitResourceURI(remoteURL, sha) + "#.warden.yaml"}
}

// normalizeRemote renders a git remote as a URL. scp-style SSH remotes
// (`git@github.com:org/repo.git`) are not URLs, so they are rewritten to the
// ssh:// form rather than emitted as-is inside one.
//
// The result carries no userinfo. A CI checkout's origin is routinely
// `https://x-access-token:<token>@github.com/…`, and a bare
// `https://<token>@github.com/…` is equally a GitHub form — so the credential
// is as often the username as the password, and both halves go. Without this
// the token reached `ResourceURI` and the policy URI verbatim, and `--sign`
// then signed it into an envelope built to be handed to somebody else.
//
// Dropping the user costs nothing here: these URIs identify the repository,
// they are not clone commands. `ssh://githost/org/repo.git` names the same
// repository as `ssh://git@githost/org/repo.git`. `warden evidence` makes the
// same call for the same reason; the two artifacts must not disagree about
// what is safe to publish.
func normalizeRemote(remote string) string {
	remote = strings.TrimSpace(remote)
	if strings.Contains(remote, "://") {
		return sanitizeRemote(remote)
	}
	// scp-style: [user@]host:path
	if at := strings.Index(remote, "@"); at >= 0 {
		if colon := strings.Index(remote[at:], ":"); colon >= 0 {
			host := remote[at+1 : at+colon]
			path := remote[at+colon+1:]
			return sanitizeRemote("ssh://" + remote[:at+1] + host + "/" + path)
		}
	}
	return remote
}
