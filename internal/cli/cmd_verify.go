package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/service"
)

// cmdVerify handles `warden verify`, the CI provenance-skip primitive. It exits
// 0 when the commit carries an intact warden validation note (CI can trust it
// and skip re-running the checks warden already ran) and non-zero otherwise
// (CI should run the checks). Designed for: `warden verify && exit 0 || make ci`.
// With --key, the note must also be signed by a pinned trusted key, turning
// provenance-skip from "a warden ran here" into "a warden I trust ran here".
func cmdVerify(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	requirementsFile := fs.String("requirements", "", "protected JSON execution requirements (single-commit only)")
	containerSteps := fs.String("container-steps", "", "require trusted container execution for these comma-separated steps (single-commit only)")
	containerImageID := fs.String("container-image-id", "", "require this inspected sha256 image ID for --container-steps")
	minProfile := fs.String("min-profile", "", "require conformance to at least this compliance profile (permissive, standard, strict), recomputed from the note's signed observations (a single commit needs --key; a --range also enforces its base ref's profile)")
	commit := fs.String("commit", "HEAD", "commit to verify")
	rangeSpec := fs.String("range", "", "gate every commit in a BASE..HEAD range (e.g. origin/main..HEAD); exits non-zero if any commit lacks trusted provenance")
	requireSigned := fs.Bool("require-signed", false, "require each note to carry a signature that verifies (implied by --key)")
	skipMerges := fs.Bool("skip-merges", true, "in --range, skip merge commits (their parents are gated individually)")
	jsonOut := fs.Bool("json", false, "emit the verdict as JSON: per-commit verdicts in --range; for one commit, its scope and validated workspaces")
	quiet := fs.Bool("quiet", false, "print nothing; communicate only via exit code")
	keys := fs.String("key", "", "comma-separated trusted signer key(s) or fingerprint(s); require a matching signature")
	// External-run attestations assert something weaker than a local one: CI ran
	// the checks and warden recorded the reference (ADR 0003). Refused unless
	// asked for, because every existing caller of this command means "warden ran
	// the checks" and upgrading must not quietly widen that.
	allowExternal := fs.Bool("allow-external", false, "also accept an attestation of an EXTERNAL CI run, not just a local warden run")
	requireExternal := fs.Bool("require-external", false, "accept ONLY an external CI-run attestation (for a branch where CI is the attester of record)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if rejectExtraArgs(fs, stderr, "verify", "commit") {
		return 2
	}

	requirementsSet := false
	containerStepsSet := false
	minProfileSet := false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "requirements":
			requirementsSet = true
		case "container-steps":
			containerStepsSet = true
		case "min-profile":
			minProfileSet = true
		}
	})
	var requiredContainerSteps []domain.StepName
	for _, name := range splitList(*containerSteps) {
		step := domain.StepName(name)
		if !step.Valid() {
			_, _ = fmt.Fprintln(stderr, "warden: invalid --container-steps name")
			return 2
		}
		requiredContainerSteps = append(requiredContainerSteps, step)
	}
	if (containerStepsSet && len(requiredContainerSteps) == 0) || (*containerImageID != "" && (len(requiredContainerSteps) == 0 || !domain.ValidContainerImageID(*containerImageID))) || (*rangeSpec != "" && len(requiredContainerSteps) > 0) {
		_, _ = fmt.Fprintln(stderr, "warden: --container-steps requires step names and single-commit verification; --container-image-id requires --container-steps and a sha256 image ID")
		return 2
	}
	var requirements domain.ExecutionRequirements
	if requirementsSet {
		if *requirementsFile == "" || *rangeSpec != "" {
			_, _ = fmt.Fprintln(stderr, "warden: --requirements needs a file and single-commit verification")
			return 2
		}
		var err error
		requirements, err = loadExecutionRequirements(*requirementsFile)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "warden: invalid requirements: %v\n", err)
			return 2
		}
	}
	mins, code := parseMinProfile(minProfileSet, *minProfile, *rangeSpec, *keys, requirements, stderr)
	if code != 0 {
		return code
	}
	svc, err := newService(autoApprover{})
	if err != nil {
		return fail(stderr, err)
	}

	policy, err := externalPolicy(*allowExternal, *requireExternal)
	if err != nil {
		return fail(stderr, err)
	}
	// A range gate resolves its roster from the BASE ref (the trusted side), so it
	// gets its own resolution path — see runVerifyRange.
	if *rangeSpec != "" {
		return runVerifyRange(svc, rangeRequest{spec: *rangeSpec, keys: *keys, requireSigned: *requireSigned, skipMerges: *skipMerges, mins: mins, policy: policy},
			*jsonOut, *quiet, stdout, stderr)
	}

	// An explicit --key wins; otherwise fall back to the committed .warden.yaml
	// roster, so a repo enforces trusted provenance without passing fingerprints.
	trusted, fromRoster := resolveTrustedKeys(svc, *keys)
	var res service.VerifyResult
	switch {
	case requirementsSet:
		res, err = svc.VerifyRequirementsWithProfiles(*commit, policy, requirements, mins, trusted...)
	case len(mins) > 0:
		res, err = svc.VerifyMinProfile(*commit, policy, mins, trusted...)
	default:
		res, err = svc.VerifyWithPolicy(*commit, policy, trusted...)
	}
	if err != nil {
		return fail(stderr, err)
	}

	containerRefused := len(requiredContainerSteps) > 0 && (!res.Validated || !res.Trusted || res.External || res.Record == nil || !res.Record.ContainerChecks(requiredContainerSteps, *containerImageID))
	if containerRefused {
		res.Validated = false
	}
	signatureMissing := *requireSigned && !res.SignatureValid && !res.CarriedTrust
	if signatureMissing {
		res.Validated = false
	}
	if *jsonOut && !*quiet {
		// Checks this command adds over the service's verification, judged
		// apart from the scope: an affected note never passes the container
		// check above, which requires a validated result.
		containerMissing := len(requiredContainerSteps) > 0 &&
			(!res.Trusted || res.External || res.Record == nil || !res.Record.ContainerChecks(requiredContainerSteps, *containerImageID))
		claim := svc.ClaimScope(res, res.Validated, !signatureMissing && !containerMissing)
		if code := printVerifyJSON(stdout, stderr, res, claim, containerMissing, signatureMissing); code != 0 {
			return code
		}
	} else if !*quiet {
		if fromRoster {
			_, _ = fmt.Fprintf(stdout, "warden: requiring a trusted signer from .warden.yaml (%d key(s))\n", len(trusted))
		}
		switch {
		case len(res.RequirementFailures) > 0:
			_, _ = fmt.Fprintf(stdout, "unverified %s — %s; run the checks\n", short(res.SHA), strings.Join(res.RequirementFailures, "; "))
		case containerRefused:
			_, _ = fmt.Fprintf(stdout, "unverified %s — required trusted container execution evidence is missing or does not match; run the checks\n", short(res.SHA))
		case signatureMissing:
			_, _ = fmt.Fprintf(stdout, "unverified %s — a valid signature is required\n", short(res.SHA))
		default:
			printVerify(stdout, res, len(trusted) > 0)
			if res.Validated {
				printConformance(stdout, res.Conformance)
			}
		}
	}
	if res.Validated {
		return 0
	}
	return 1
}

// parseMinProfile resolves --min-profile and refuses the invocations a minimum
// profile cannot be checked under. It returns a non-zero exit code on refusal.
//
// A minimum profile is a consumer-side control, so for a single commit it
// demands an explicitly pinned --key: the roster fallback reads .warden.yaml
// from the checkout under test, which is the input being judged. The same holds
// for a version 2 requirements file that names a profile. A --range is
// different: it reads its roster from the BASE ref, the trusted side, so it
// takes the flag without --key; with neither a key nor a base roster every
// noted commit fails the profile's trust clause, and says so.
func parseMinProfile(set bool, value, rangeSpec, keys string, requirements domain.ExecutionRequirements, stderr io.Writer) (mins []domain.ComplianceProfile, code int) {
	if set {
		p, err := domain.ParseComplianceProfile(value)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "warden: invalid --min-profile: %v\n", err)
			return nil, 2
		}
		mins = append(mins, p)
	}
	if rangeSpec != "" {
		return mins, 0
	}
	if (len(mins) > 0 || requirements.Profile != nil) && len(splitList(keys)) == 0 {
		_, _ = fmt.Fprintln(stderr, "warden: a minimum profile needs a pinned --key; a roster read from the checkout under test cannot vouch for that checkout")
		return nil, 2
	}
	return mins, 0
}

// printConformance lists, for a passing note, exactly which profile clauses
// were checked and which this release leaves unchecked, so a pass is never
// read as covering more than it did.
func printConformance(w io.Writer, reports []service.ConformanceReport) {
	for _, r := range reports {
		_, _ = fmt.Fprintf(w, "  conforms to %s; checked: %s\n", r.Min, strings.Join(r.Checked, "; "))
		if len(r.Deferred) > 0 {
			_, _ = fmt.Fprintf(w, "  not checked by this release: %s\n", strings.Join(r.Deferred, "; "))
		}
	}
}

// resolveTrustedKeys returns the effective trusted-signer set for a
// single-commit `warden verify` and whether it came from the committed roster.
// An explicit --key list wins outright; otherwise the working-tree .warden.yaml
// `trusted_keys` roster supplies it, so committing a roster turns on
// trusted-signed enforcement without a flag on every call. A config-load error
// degrades to "no roster" — verify never hard-fails on it.
//
// Reading the roster from the working tree is safe here because single-commit
// verify is a local convenience (you trust your own checkout). CI provenance-skip
// that gates a commit it did not produce should pin --key explicitly rather than
// trust an in-tree roster; a range gate does this structurally by reading the
// roster from its base ref (see resolveRangeTrustedKeys).
func resolveTrustedKeys(svc *service.Service, keyFlag string) (keys []string, fromRoster bool) {
	if strings.TrimSpace(keyFlag) != "" {
		return splitList(keyFlag), false
	}
	cfg, err := svc.Config()
	if err != nil || len(cfg.TrustedKeys) == 0 {
		return nil, false
	}
	return cfg.TrustedKeys, true
}

// runVerifyRange gates a BASE..HEAD range: it exits 0 only when every commit
// carries provenance to the required depth, and non-zero (with per-commit
// reasons) otherwise. This is the primitive a PR required-check or a
// pre-receive hook wraps — see docs/adr/0002. The CLI passes intent (an explicit
// --key, or "use the committed roster"); the service resolves the roster from the
// trusted BASE ref, so the trust invariant lives in the core, not here.
func runVerifyRange(svc *service.Service, req rangeRequest, jsonOut, quiet bool, stdout, stderr io.Writer) int {
	base, head, ok := parseRange(req.spec)
	if !ok {
		_, _ = fmt.Fprintf(stderr, "warden: --range must be BASE..HEAD (two-dot), e.g. origin/main..HEAD; got %q\n", req.spec)
		return 2
	}
	opts := service.RangeVerifyOptions{
		RequireSigned:  req.requireSigned,
		SkipMerges:     req.skipMerges,
		ExternalPolicy: req.policy,
		MinProfiles:    req.mins,
	}
	if k := strings.TrimSpace(req.keys); k != "" {
		opts.TrustedKeys = splitList(k) // explicit pin wins outright
	} else {
		opts.UseRoster = true // let the service read the roster from the base ref
	}
	res, err := svc.VerifyRange(base, head, opts)
	if err != nil {
		return fail(stderr, err)
	}
	if !quiet {
		if res.RosterFromBase && !jsonOut {
			_, _ = fmt.Fprintf(stdout, "warden: requiring a trusted signer from %s:.warden.yaml (%d key(s))\n", base, len(res.Effective.TrustedKeys))
		}
		if !jsonOut {
			for _, p := range res.Profiles {
				_, _ = fmt.Fprintf(stdout, "warden: requiring compliance profile %s (%s)\n", p.Profile, profileSource(p, base))
			}
		}
		if jsonOut {
			if code := printRangeJSON(stdout, stderr, res); code != 0 {
				return code
			}
		} else {
			printRange(stdout, res)
		}
	}
	if res.OK() {
		return 0
	}
	return 1
}

// rangeRequest is what `verify --range` asks the range gate for.
type rangeRequest struct {
	spec, keys    string
	requireSigned bool
	skipMerges    bool
	mins          []domain.ComplianceProfile
	policy        service.ExternalPolicy
}

// profileSource says where a range gate's profile came from, for the human
// report.
func profileSource(p service.RangeProfile, base string) string {
	switch {
	case p.FromBase && p.FromFlag:
		return "from " + base + ":.warden.yaml and --min-profile"
	case p.FromBase:
		return "from " + base + ":.warden.yaml"
	default:
		return "from --min-profile"
	}
}

// parseRange splits a two-dot "BASE..HEAD" spec. It rejects the three-dot
// symmetric-difference form (git's `A...B`) — a provenance gate must walk a
// definite ancestry range, not a symmetric diff — and any spec missing an
// endpoint.
func parseRange(spec string) (base, head string, ok bool) {
	if strings.Contains(spec, "...") {
		return "", "", false
	}
	b, h, found := strings.Cut(spec, "..")
	if !found || b == "" || h == "" {
		return "", "", false
	}
	return b, h, true
}

// printVerify renders a verify result, including signature provenance.
func printVerify(w io.Writer, res service.VerifyResult, pinned bool) {
	if res.Validated {
		_, _ = fmt.Fprintf(w, "validated %s", short(res.SHA))
		if res.Record != nil {
			_, _ = fmt.Fprintf(w, " (%s, %d steps, chain-intact", res.Record.RunID, len(res.Record.StepsRun))
			_, _ = fmt.Fprintf(w, "%s)", signerNote(res))
			if res.Record.Scope != nil {
				_, _ = fmt.Fprintf(w, "\n  scope: %s", res.Record.Scope.Summary())
			}
		}
		_, _ = fmt.Fprintln(w)
		return
	}
	// A sound note refused only for its KIND is not a missing note. Reporting
	// "no intact warden note" about one sitting right there sends the reader
	// hunting for the wrong thing entirely.
	if res.ExternalRefused && res.External {
		_, _ = fmt.Fprintf(w, "unverified %s — carries an EXTERNAL CI-run attestation, which is not "+
			"accepted by default; pass --allow-external to accept it\n", short(res.SHA))
		return
	}
	if res.ExternalRefused && !res.External {
		_, _ = fmt.Fprintf(w, "unverified %s — --require-external was given and this is a local warden "+
			"attestation, not a record of a CI run\n", short(res.SHA))
		return
	}
	// Refused only for its scope: a sound, trusted note of a partial check.
	// Name it as that, with the scope it records, rather than as unverified
	// for some reason the reader would go looking for.
	if res.ScopeRefused && res.Record != nil {
		_, _ = fmt.Fprintf(w, "unverified %s — %s: the note attests an affected-scope run (%s), "+
			"not a full validation; verifiers refuse it by default; run the checks\n",
			short(res.SHA), domain.ReasonAffectedScope, res.Record.Scope.Summary())
		return
	}
	// THERE IS NO NOTE. This has to be settled before any branch that describes
	// a note's properties, because `Signed` is false for two different worlds:
	// a note carrying no signature, and no note at all. With a pinned roster the
	// second was reaching `pinned && !res.Signed` and being reported as the
	// first — telling the reader to go looking for an unsigned note that does
	// not exist.
	//
	// Record is the exact discriminator: VerifyWithPolicy returns an ERROR when
	// the note cannot be read, so a nil Record means absent, never unreadable.
	if res.Record == nil {
		_, _ = fmt.Fprintf(w, "unverified %s — no warden note (pushed with --no-verify, "+
			"or made outside warden); run the checks\n", short(res.SHA))
		return
	}
	// Give a specific reason when a pinned key is what failed.
	if pinned && res.Signed && res.SignatureValid && !res.Trusted {
		_, _ = fmt.Fprintf(w, "unverified %s — signed by untrusted key %s; run the checks\n", short(res.SHA), res.Signer)
		return
	}
	if pinned && res.Signed && !res.SignatureValid {
		_, _ = fmt.Fprintf(w, "unverified %s — signature does not verify; run the checks\n", short(res.SHA))
		return
	}
	if pinned && !res.Signed {
		_, _ = fmt.Fprintf(w, "unverified %s — note is unsigned but a trusted key was required; run the checks\n", short(res.SHA))
		return
	}
	// A note exists, and the signature is not what failed. What is left is that
	// it does not ATTEST this commit: an empty evidence chain, or a record bound
	// to a different commit because history was rewritten under it. Saying "no
	// intact warden note" about a note sitting right there is the same wrong
	// diagnosis in the opposite direction — `warden doctor` calls this case
	// UNBOUND, and `warden why` will print the record.
	_, _ = fmt.Fprintf(w, "unverified %s — a warden note exists but does not attest this commit "+
		"(empty evidence chain, or bound to another commit after a rewrite); run `warden why %s`\n",
		short(res.SHA), short(res.SHA))
}

// gateDepth names, for the human summary, how strict this gate was — so a green
// result is not mistaken for more assurance than it checked. It reads the
// EFFECTIVE options (roster already resolved from the base ref).
func gateDepth(opts service.RangeVerifyOptions) string {
	switch {
	case len(opts.TrustedKeys) > 0:
		return "trusted-signed"
	case opts.RequireSigned:
		return "signed"
	default:
		return "attested"
	}
}

// reasonHint turns a machine reason into a one-line explanation for the human
// report.
func reasonHint(r domain.VerifyReason) string {
	switch r {
	case domain.ReasonExternal:
		return "attestation kind does not satisfy the external-run policy"
	case domain.ReasonMissing:
		return "no warden note (pushed with --no-verify, or made outside warden)"
	case domain.ReasonUnreadable:
		return "warden note is present but could not be decoded (malformed JSON — a hand-edit, or `git notes merge` concatenating two records); restore the blob, re-committing will not fix it"
	case domain.ReasonBrokenChain:
		return "note present but does not attest this commit (broken/transplanted)"
	case domain.ReasonUnsigned:
		return "note is unsigned or its signature does not verify"
	case domain.ReasonUntrusted:
		return "signed by a key outside the trusted set"
	case domain.ReasonProfile:
		return "provenance is sound, but the note does not conform to a required compliance profile"
	case domain.ReasonAffectedScope:
		return "provenance is sound, but the note attests an affected-scope run (per-workspace checks for the affected workspaces only), which is not accepted as a full validation; a push span it covers is refused with it"
	case domain.ReasonForgeAuthored:
		return "authored by the forge (squash merge, web edit, or a bot), signed by its key — no developer machine was in this commit's path, so no local gate could have run; allow these with forge.accept_authored in .warden.yaml"
	default:
		return "ok"
	}
}

// countCovered tallies commits that passed via a covering push-span rather
// than a note of their own.
func countCovered(res service.RangeVerifyResult) int {
	n := 0
	for i := range res.Commits {
		if res.Commits[i].CoveredBy != "" {
			n++
		}
	}
	return n
}

// countProfileUnchecked tallies commits that passed while a profile applied
// without being checked against it (forge-accepted: no record to recompute).
func countProfileUnchecked(res service.RangeVerifyResult) int {
	n := 0
	for i := range res.Commits {
		if res.Commits[i].ProfileUnchecked {
			n++
		}
	}
	return n
}

// countForgeAccepted tallies commits that passed because the forge signed them,
// not because warden gated them.
func countForgeAccepted(res service.RangeVerifyResult) int {
	n := 0
	for i := range res.Commits {
		if res.Commits[i].ForgeSigner != "" {
			n++
		}
	}
	return n
}

// printRange renders the human-readable range-gate result: a per-failure line
// for anything that did not pass, then a one-line verdict naming the depth.
func printRange(w io.Writer, res service.RangeVerifyResult) {
	fails := res.Failures()
	for _, v := range fails {
		_, _ = fmt.Fprintf(w, "  ✗ %s — %s\n", short(v.SHA), reasonHint(v.Reason))
		for _, f := range v.ProfileFailures {
			_, _ = fmt.Fprintf(w, "      %s\n", f)
		}
	}
	if len(fails) == 0 {
		_, _ = fmt.Fprintf(w, "verified %d commit(s) in %s..%s (%s)\n", len(res.Commits), short(res.Base), short(res.Head), gateDepth(res.Effective))
		// Say how many passed by span rather than by their own note, so a reader
		// can tell "individually attested" from "published by a gated push" —
		// green should not hide which claim is being made.
		if n := countCovered(res); n > 0 {
			_, _ = fmt.Fprintf(w, "  (%d covered by a gated push's signed span, not individually attested)\n", n)
		}
		// A forge-accepted commit was never gated by anything. Reporting it
		// beside the total would let a green range imply checks that did not
		// run, which is the one thing this gate exists not to do.
		if n := countForgeAccepted(res); n > 0 {
			_, _ = fmt.Fprintf(w, "  (%d accepted as forge-authored — the forge signed them; warden ran NO checks on them)\n", n)
		}
		// Name the profiles only when one applied: without one, the gate is the
		// gate it always was and must not read as profile-checked.
		if len(res.Profiles) > 0 {
			_, _ = fmt.Fprintf(w, "  every noted commit conforms to %s\n", profileList(res.Profiles))
		}
		if n := countProfileUnchecked(res); n > 0 {
			_, _ = fmt.Fprintf(w, "  (%d forge-accepted commit(s) NOT profile-checked — no warden record to recompute)\n", n)
		}
		return
	}
	want := gateDepth(res.Effective) + " provenance"
	if len(res.Profiles) > 0 {
		want += " conforming to " + profileList(res.Profiles)
	}
	_, _ = fmt.Fprintf(w, "FAILED: %d of %d commit(s) in %s..%s lack %s\n", len(fails), len(res.Commits), short(res.Base), short(res.Head), want)
}

// profileList joins a range gate's profiles for one line of output.
func profileList(ps []service.RangeProfile) string {
	names := make([]string, len(ps))
	for i, p := range ps {
		names[i] = p.Profile.String()
	}
	return strings.Join(names, " and ")
}

// rangeExport is the stable JSON shape a CI job or the Phase-2 action consumes.
// Profiles is additive: absent when no profile was enforced.
type rangeExport struct {
	Base     string                 `json:"base"`
	Head     string                 `json:"head"`
	Depth    string                 `json:"depth"`
	OK       bool                   `json:"ok"`
	Total    int                    `json:"total"`
	Failed   int                    `json:"failed"`
	Profiles []rangeProfileExport   `json:"profiles,omitempty"`
	Commits  []domain.CommitVerdict `json:"commits"`
}

// rangeProfileExport is one enforced minimum profile and where it came from.
type rangeProfileExport struct {
	Profile  string `json:"profile"`
	FromBase bool   `json:"from_base"`
	FromFlag bool   `json:"from_flag"`
}

// exportProfiles converts a range gate's profiles for JSON output.
func exportProfiles(ps []service.RangeProfile) []rangeProfileExport {
	var out []rangeProfileExport
	for _, p := range ps {
		out = append(out, rangeProfileExport{Profile: p.Profile.String(), FromBase: p.FromBase, FromFlag: p.FromFlag})
	}
	return out
}

// printRangeJSON emits the range result as JSON. It returns a non-zero CLI code
// only on an encode failure; the pass/fail exit is decided by the caller from
// res.OK so --json and text share one exit contract.
func printRangeJSON(stdout, stderr io.Writer, res service.RangeVerifyResult) int {
	out := rangeExport{
		Base:     res.Base,
		Head:     res.Head,
		Depth:    gateDepth(res.Effective),
		OK:       res.OK(),
		Total:    len(res.Commits),
		Failed:   len(res.Failures()),
		Profiles: exportProfiles(res.Profiles),
		Commits:  res.Commits,
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return fail(stderr, err)
	}
	return 0
}

// signerNote describes the signature state for a validated line.
func signerNote(res service.VerifyResult) string {
	switch {
	case res.CarriedTrust:
		// Say whose run this actually was, and on which commit. Printing "signed by
		// trusted <fp>" here would be the misreading #212 §7 already fell for once:
		// it looks like the note in front of you was signed by that key, when in
		// fact the key signed a DIFFERENT commit and this note inherits the claim
		// through identical trees.
		src, signer := "", ""
		if res.Record != nil {
			src = short(res.Record.ReattestedFrom)
			if orig := res.Record.CarriedOriginal; orig != nil {
				signer = orig.SignerFingerprint()
			}
		}
		return fmt.Sprintf(", re-attested from %s whose run was signed by trusted %s (identical trees)", src, signer)
	case res.Trusted:
		return fmt.Sprintf(", signed by trusted %s", res.Signer)
	case res.SignatureValid:
		return fmt.Sprintf(", signed by %s", res.Signer)
	case res.Signed:
		return ", signature INVALID"
	default:
		return ", unsigned"
	}
}

// externalPolicy maps the two flags onto a policy, refusing the combination that
// asks for both at once.
//
// --allow-external and --require-external are not a spectrum a caller can hold
// simultaneously: one widens what is accepted and the other narrows it. Picking
// a winner silently would mean a gate enforcing something other than what its
// invocation says, which is the failure this whole area keeps producing.
func externalPolicy(allow, require bool) (service.ExternalPolicy, error) {
	switch {
	case allow && require:
		return service.ExternalReject, fmt.Errorf(
			"--allow-external and --require-external are contradictory: one widens what is accepted, the other narrows it")
	case require:
		return service.ExternalRequire, nil
	case allow:
		return service.ExternalAllow, nil
	default:
		return service.ExternalReject, nil
	}
}

// loadExecutionRequirements bounds policy input and rejects unknown fields and trailing JSON.
func loadExecutionRequirements(path string) (domain.ExecutionRequirements, error) {
	var result domain.ExecutionRequirements
	f, err := os.Open(path)
	if err != nil {
		return result, err
	}
	defer func() { _ = f.Close() }()
	const limit = 1 << 20
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return result, err
	}
	if len(data) > limit {
		return result, fmt.Errorf("file exceeds 1 MiB")
	}
	if err := rejectDuplicateJSONKeys(json.NewDecoder(strings.NewReader(string(data)))); err != nil {
		return result, err
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&result); err != nil {
		return result, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return result, fmt.Errorf("expected a single JSON object")
	}
	return result, result.Validate()
}

// Reject ambiguous policy objects rather than accepting the last duplicate key.
func rejectDuplicateJSONKeys(dec *json.Decoder) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]bool)
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok {
				return fmt.Errorf("invalid JSON object key")
			}
			if seen[name] {
				return fmt.Errorf("duplicate JSON object key %q", name)
			}
			seen[name] = true
			if err := rejectDuplicateJSONKeys(dec); err != nil {
				return err
			}
		}
	case '[':
		for dec.More() {
			if err := rejectDuplicateJSONKeys(dec); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter")
	}
	_, err = dec.Token()
	return err
}

// verifyExport is single-commit `verify --json`: the verdict, and what CI may
// rely on per workspace (design 0006, Phase B). The exit code is the same as
// without --json; the warden-verify action reads its scope outputs from here
// rather than from the human text.
type verifyExport struct {
	Commit    string `json:"commit"`
	Validated bool   `json:"validated"`
	Trusted   bool   `json:"trusted"`
	Signer    string `json:"signer,omitempty"`
	External  bool   `json:"external"`
	// Scope is "full" when the commit validated, "affected" when a sound,
	// trusted note was refused only for attesting an affected-scope run, and
	// "none" otherwise.
	Scope service.ScopeClaimKind `json:"scope"`
	// ValidatedWorkspaces are the workspaces checked on exactly this tree:
	// every one for a full scope, the selection for an affected one, none
	// otherwise. Always an array.
	ValidatedWorkspaces []string `json:"validated_workspaces"`
	// Failures names the checks a refused note failed, for a reader.
	Failures []string `json:"failures,omitempty"`
	// RecordScope is the scope the note records, when the claim rests on it.
	RecordScope *domain.RunScope `json:"record_scope,omitempty"`
}

func printVerifyJSON(stdout, stderr io.Writer, res service.VerifyResult, claim service.ScopeClaim, containerMissing, signatureMissing bool) int {
	out := verifyExport{
		Commit:              res.SHA,
		Validated:           res.Validated,
		Trusted:             res.Trusted,
		Signer:              res.Signer,
		External:            res.External,
		Scope:               claim.Kind,
		ValidatedWorkspaces: claim.ValidatedWorkspaces,
		Failures:            append([]string{}, res.RequirementFailures...),
	}
	if claim.Kind != service.ScopeClaimNone && res.Record != nil {
		out.RecordScope = res.Record.Scope
	}
	switch {
	case res.Validated:
		out.Failures = nil
	case res.ScopeRefused:
		out.Failures = append(out.Failures, string(domain.ReasonAffectedScope))
	}
	if containerMissing && !res.Validated {
		out.Failures = append(out.Failures, "required trusted container execution evidence is missing or does not match")
	}
	if signatureMissing {
		out.Failures = append(out.Failures, "a valid signature is required")
	}
	if !res.Validated && len(out.Failures) == 0 {
		out.Failures = append(out.Failures, "no intact, trusted note attests this commit")
	}
	return writeJSON(stdout, stderr, out)
}
