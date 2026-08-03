package cli

import (
	"context"
	"flag"
	"fmt"
	"io"

	"go.klarlabs.de/axi/toon"

	"go.klarlabs.de/warden/internal/domain"
	mcpserver "go.klarlabs.de/warden/internal/mcp"
)

// cmdAxi is the flags-only, non-interactive agent surface (§4.6). It emits TOON
// (Token-Optimized Object Notation) on stdout rather than JSON, so an agent
// consuming it spends ~40% fewer tokens on the uniform result shapes.
func cmdAxi(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		_, _ = fmt.Fprintln(stderr, "usage: warden axi <verify|verify-range|doctor|audit|status|policy-explain|steps|run-trigger> [flags]")
		return 2
	}
	verb, rest := args[0], args[1:]

	f, err := newFacade()
	if err != nil {
		return fail(stderr, err)
	}

	switch verb {
	case "policy-explain":
		return axiPolicyExplain(f, rest, stdout, stderr)
	case "steps":
		return axiSteps(f, stdout, stderr)
	case "run-trigger":
		return axiRunTrigger(f, rest, stdout, stderr)
	case "verify":
		return axiVerify(f, rest, stdout, stderr)
	case "verify-range":
		return axiVerifyRange(f, rest, stdout, stderr)
	case "doctor":
		return axiAuditReport(f.Doctor, rest, stdout, stderr)
	case "audit":
		return axiAuditReport(f.Audit, rest, stdout, stderr)
	case "status":
		return axiStatus(f, stdout, stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "warden axi: unknown verb %q\n", verb)
		return 2
	}
}

// axiVerify answers "is this commit gated?" — the provenance-skip primitive.
//
// It exits non-zero when the commit is NOT validated, so a shell caller can use
// it as a gate directly (`warden axi verify && skip-the-checks`) without parsing
// the payload, while an agent reads the same answer from the emitted fields.
func axiVerify(f facade, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("axi verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	commitFlag := fs.String("commit", "HEAD", "commit-ish to verify")
	keysFlag := fs.String("key", "", "comma-separated trusted keys or fingerprints")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if rejectExtraArgs(fs, stderr, "axi verify", "commit") {
		return 2
	}
	rec, err := f.Verify(*commitFlag, splitList(*keysFlag))
	if err != nil {
		return fail(stderr, err)
	}
	out := map[string]any{
		"sha":             *commitFlag,
		"validated":       rec.Validated,
		"signed":          rec.Signed,
		"signature_valid": rec.SignatureValid,
		"signer":          rec.Signer,
		"trusted":         rec.Trusted,
	}
	if rec.Record != nil {
		out["run_id"] = rec.Record.RunID
		out["steps"] = stepStrings(rec.Record.StepsRun)
		out["warden_version"] = rec.Record.WardenVersion
	}
	if code := emitTOON(stdout, stderr, out); code != 0 {
		return code
	}
	// The verdict belongs in the exit status too: this verb's whole purpose is to
	// be a gate, and a gate that always exits 0 gates nothing.
	if !rec.Validated {
		return 1
	}
	return 0
}

// axiVerifyRange gates a whole base..head span, exiting non-zero if any commit
// lacks trustworthy provenance — the shape a PR check needs.
func axiVerifyRange(f facade, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("axi verify-range", flag.ContinueOnError)
	fs.SetOutput(stderr)
	baseFlag := fs.String("base", "", "base ref of the range (required)")
	headFlag := fs.String("head", "HEAD", "head ref of the range")
	signedFlag := fs.Bool("require-signed", false, "fail a commit whose note is unsigned")
	keysFlag := fs.String("key", "", "comma-separated trusted keys or fingerprints")
	skipMergesFlag := fs.Bool("skip-merges", true, "skip merge commits; their parents are gated individually")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if rejectExtraArgs(fs, stderr, "axi verify-range", "range") {
		return 2
	}
	if *baseFlag == "" {
		_, _ = fmt.Fprintln(stderr, "warden axi verify-range: --base is required")
		return 2
	}
	keys := splitList(*keysFlag)
	res, err := f.VerifyRange(*baseFlag, *headFlag, mcpserver.RangeVerifyRequest{
		RequireSigned: *signedFlag,
		TrustedKeys:   keys,
		// With no keys pinned, resolve the roster from the BASE ref — the trusted
		// side. A range gate must never take its trust anchor from the head it is
		// checking.
		UseRoster:  len(keys) == 0,
		SkipMerges: *skipMergesFlag,
	})
	if err != nil {
		return fail(stderr, err)
	}
	commits := make([]any, 0, len(res.Commits))
	for _, c := range res.Commits {
		m := map[string]any{"sha": c.SHA, "reason": string(c.Reason)}
		if c.CoveredBy != "" {
			m["covered_by"] = c.CoveredBy
		}
		commits = append(commits, m)
	}
	if code := emitTOON(stdout, stderr, map[string]any{
		"base":             res.Base,
		"head":             res.Head,
		"ok":               res.OK,
		"failed":           res.Failed,
		"require_signed":   res.RequireSigned,
		"roster_from_base": res.RosterFromBase,
		"commits":          commits,
	}); code != 0 {
		return code
	}
	if !res.OK {
		return 1
	}
	return 0
}

// axiAuditReport serves both `doctor` and `audit`: the two differ in which
// commits they walk, not in what they report, so one renderer keeps the emitted
// schema identical for both.
func axiAuditReport(report func(string) (domain.AuditReport, error), args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("axi audit", flag.ContinueOnError)
	fs.SetOutput(stderr)
	branchFlag := fs.String("branch", "", "branch to report on (default current)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if rejectExtraArgs(fs, stderr, "axi audit", "branch") {
		return 2
	}
	rep, err := report(*branchFlag)
	if err != nil {
		return fail(stderr, err)
	}
	t := rep.Counts()
	commits := make([]any, 0, len(rep.Commits))
	// Index rather than range-copy: CommitStatus is 128 bytes and an audit walks
	// every commit since adoption.
	for i := range rep.Commits {
		c := &rep.Commits[i]
		m := map[string]any{
			"sha":          c.SHA,
			"subject":      c.Subject,
			"has_note":     c.HasNote,
			"chain_intact": c.ChainIntact,
		}
		if c.ReattestableFrom != "" {
			m["reattestable_from"] = c.ReattestableFrom
		}
		commits = append(commits, m)
	}
	return emitTOON(stdout, stderr, map[string]any{
		"adoption":     rep.Adoption,
		"branch":       rep.Branch,
		"verified":     t.Verified,
		"defective":    t.Defective,
		"unverified":   t.Unverified,
		"covered":      t.Covered,
		"unknown":      t.Unknown,
		"reattestable": len(rep.Reattestable()),
		"commits":      commits,
	})
}

// axiStatus reports whether the gate is actually armed — a repo can carry a
// .warden.yaml and still have no installed hook, which is configured, not gated.
func axiStatus(f facade, stdout, stderr io.Writer) int {
	st, err := f.Status()
	if err != nil {
		return fail(stderr, err)
	}
	hooks := make(map[string]any, len(st.InstalledHooks))
	for name, on := range st.InstalledHooks {
		hooks[name] = on
	}
	return emitTOON(stdout, stderr, map[string]any{
		"version":         st.Version,
		"repo_dir":        st.RepoDir,
		"adoption":        st.Adoption,
		"installed_hooks": hooks,
		"pre_commit":      stepStrings(st.PreCommit),
		"pre_push":        stepStrings(st.PrePush),
		"signing_key":     st.SigningKey,
	})
}

func axiPolicyExplain(f facade, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("axi policy-explain", flag.ContinueOnError)
	fs.SetOutput(stderr)
	hookFlag := fs.String("hook", "pre-push", "hook")
	branchFlag := fs.String("branch", "", "branch")
	pathsFlag := fs.String("paths", "", "comma-separated paths")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if rejectExtraArgs(fs, stderr, "axi policy-explain", "") {
		return 2
	}
	hook, err := domain.ParseHook(*hookFlag)
	if err != nil {
		return fail(stderr, err)
	}
	resolved, err := f.PolicyExplain(hook, *branchFlag, splitList(*pathsFlag))
	if err != nil {
		return fail(stderr, err)
	}
	return emitTOON(stdout, stderr, map[string]any{
		"hook":             string(resolved.Hook),
		"risk":             string(resolved.Risk),
		"require_approval": resolved.RequireApproval,
		"steps":            stepStrings(resolved.Steps),
		"matched_rules":    anyStrings(resolved.MatchedRules),
	})
}

func axiSteps(f facade, stdout, stderr io.Writer) int {
	pre, push, err := f.StepsList()
	if err != nil {
		return fail(stderr, err)
	}
	return emitTOON(stdout, stderr, map[string]any{
		"pre_commit": stepStrings(pre),
		"pre_push":   stepStrings(push),
	})
}

func axiRunTrigger(f facade, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("axi run-trigger", flag.ContinueOnError)
	fs.SetOutput(stderr)
	hookFlag := fs.String("hook", "pre-push", "hook")
	trustFlag := fs.Bool("trust", false, "trust this repo and run its configured commands (also WARDEN_MCP_ALLOW_RUN=1)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if rejectExtraArgs(fs, stderr, "axi run-trigger", "") {
		return 2
	}
	// run-trigger executes repo-authored shell on the auto-approved path, so
	// refuse unless the operator explicitly trusts this repo. Checked before
	// hook parsing so the refusal is deterministic.
	if !mcpRunTrusted(*trustFlag) {
		return fail(stderr, errUntrustedMCPRun())
	}
	hook, err := domain.ParseHook(*hookFlag)
	if err != nil {
		return fail(stderr, err)
	}
	summary, err := f.RunTrigger(context.Background(), hook)
	if err != nil {
		return fail(stderr, err)
	}
	return emitTOON(stdout, stderr, map[string]any{
		"outcome": summary.Outcome,
		"hook":    summary.Hook,
		"message": summary.Message,
		"run_id":  summary.RunID,
		// findings/blocker/retryable are what make a failure actionable. Without
		// them the verb reports only THAT something went wrong, leaving an agent
		// to re-run the gate interactively to discover what — which is exactly
		// the surface it does not have.
		"findings":  findingMaps(summary.Findings),
		"blocker":   summary.Blocker,
		"retryable": summary.Retryable,
	})
}

// findingMaps renders findings as the uniform map shape TOON encodes, using the
// same field names as the MCP surface's JSON so an agent reading both surfaces
// learns one schema rather than two.
func findingMaps(findings []domain.Finding) []any {
	out := make([]any, len(findings))
	for i, f := range findings {
		m := map[string]any{
			"severity": string(f.Severity),
			"message":  f.Message,
		}
		if f.File != "" {
			m["file"] = f.File
		}
		if f.Line > 0 {
			m["line"] = f.Line
		}
		if f.Rule != "" {
			m["rule"] = f.Rule
		}
		if f.Why != "" {
			m["why"] = f.Why
		}
		// The remediation is the half an agent can act on: with it, a failed run
		// becomes run → read → fix → re-run instead of run → guess → re-run.
		if f.Fix != nil {
			fix := map[string]any{}
			if f.Fix.Command != "" {
				fix["command"] = f.Fix.Command
			}
			if f.Fix.Patch != "" {
				fix["patch"] = f.Fix.Patch
			}
			if len(fix) > 0 {
				m["fix"] = fix
			}
		}
		out[i] = m
	}
	return out
}

func emitTOON(stdout, stderr io.Writer, v any) int {
	out, err := toon.Encode(v)
	if err != nil {
		return fail(stderr, err)
	}
	_, _ = fmt.Fprintln(stdout, out)
	return 0
}

func stepStrings(steps []domain.StepName) []any {
	out := make([]any, len(steps))
	for i, s := range steps {
		out[i] = string(s)
	}
	return out
}

// anyStrings widens a []string to []any, which is the slice shape TOON encodes.
func anyStrings(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
