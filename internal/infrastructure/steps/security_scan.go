package steps

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/infrastructure/scanner"
)

// SecurityScanStep runs the repo's configured security scanner and decides what
// the result means.
//
// It exists because gating on the scanner's exit code answers the wrong
// question. The exit code says "this tree has unwaived findings"; the gate
// wants to know "did *this change* add one". Those differ by the repo's whole
// historical backlog, and blocking an unrelated YAML edit on 71 inherited
// dependency CVEs does not get the CVEs fixed — it gets the push retried with
// `--no-verify`, which removes both the protection and the evidence that the
// check ever ran. So in the default delta mode the step fails only on findings
// absent from the merge-base and reports the rest as a counted warning.
//
// It also refuses to scan at all when the scanner on PATH is a different
// version from the one CI pins, because a scanner that renumbers rules between
// releases gives the same hit a different fingerprint, and every baseline entry
// stops matching at once — turning a fully triaged repo into hundreds of
// phantom criticals at the next release.
//
// Everything it does is best-effort in one direction only: whenever warden
// cannot read the scanner's report, work out a base, or scan the base tree, it
// falls back to the strict exit-code behavior it replaced. Degrading toward
// "fail" keeps a bug here from quietly opening the gate.
type SecurityScanStep struct {
	name  domain.StepName
	shell ShellStep
}

// NewSecurityScanStep binds the step to the command key it runs (its own name).
func NewSecurityScanStep(name domain.StepName) SecurityScanStep {
	return SecurityScanStep{name: name, shell: NewShellStep(name, string(name))}
}

func (s SecurityScanStep) Name() domain.StepName { return s.name }

func (s SecurityScanStep) Run(ctx context.Context, sc application.StepContext) (domain.StepResult, error) {
	command := strings.TrimSpace(sc.Commands[string(s.name)])
	scan, recognized := scanner.ParseCommand(command)
	if !recognized {
		// Not a scanner warden can read: an empty command (advisory skip), a
		// `make audit`, an `npm audit`, or a nox invocation that directs its own
		// output. Run it exactly as before and gate on its exit code.
		return s.shell.Run(ctx, sc)
	}

	dir := stepDir(sc, s.name)
	cfg := sc.SecurityScan

	// Version drift is DETECTED here but not acted on yet. It is a proxy for the
	// thing that actually matters — whether the committed baseline still matches
	// what the scanner reports — and that can only be known after scanning. See
	// resolveVersionDrift below.
	versionDrift := s.detectVersionDrift(ctx, dir, scan.Binary, cfg)

	reportDir, err := os.MkdirTemp("", "warden-scan-")
	if err != nil {
		return domain.StepResult{}, fmt.Errorf("%s: create report dir: %w", s.name, err)
	}
	defer func() { _ = os.RemoveAll(reportDir) }()

	out, runErr, contended := s.shell.runIn(ctx, sc, dir, scan.WithReportDir(reportDir))
	report, readErr := scanner.ReadReport(reportDir)
	if readErr != nil {
		// The scan produced nothing warden can read — it crashed, was killed by
		// the step timeout, or wrote a report shape this version does not know.
		// Gate on the exit code, which is what warden did before delta gating.
		return s.shell.resultFor(ctx, sc, out, runErr, contended), nil
	}

	gating := report.Gating(scan.Threshold)
	baselineDrifted, warnings := s.baselineDriftFindings(dir, report, len(gating))

	if res, refused := s.resolveVersionDrift(versionDrift, baselineDrifted); refused {
		return res, nil
	} else if versionDrift.drifted {
		warnings = append(warnings, note(versionDrift.toleratedMessage()))
	}

	if len(gating) == 0 {
		if runErr != nil {
			// The scanner failed for a reason that is not a finding warden can
			// see (`-fail-on-degraded`, a plugin error). Keep failing: "I could
			// not check" is not "the tree is clean".
			return s.shell.resultFor(ctx, sc, out, runErr, contended), nil
		}
		return s.pass(fmt.Sprintf("%s passed", s.name), warnings), nil
	}

	if cfg.ResolvedMode() == domain.ScanModeTotal {
		return s.fail(gating, nil, warnings, "total"), nil
	}
	return s.runDelta(ctx, sc, dir, scan, gating, warnings), nil
}

// runDelta narrows the gating findings to the ones this change introduced.
func (s SecurityScanStep) runDelta(ctx context.Context, sc application.StepContext, dir string, scan scanner.Command, gating []scanner.Finding, warnings []domain.Finding) domain.StepResult {
	baseSHA, err := resolveBaseSHA(ctx, dir, sc.Branch, sc.SecurityScan.Base)
	if err != nil {
		return s.fail(gating, nil, append(warnings, note(
			"could not determine a base commit to compare against ("+err.Error()+"), so every finding is being "+
				"treated as new; set security_scan.base to the ref this branch forked from")), "delta-no-base")
	}

	baseFingerprints, err := s.baseFingerprints(ctx, sc, dir, scan, baseSHA)
	if err != nil {
		return s.fail(gating, nil, append(warnings, note(
			"could not scan the base commit "+short(baseSHA)+" ("+err.Error()+"), so every finding is being treated as new")), "delta-base-failed")
	}

	introduced, preexisting := scanner.SplitIntroduced(gating, baseFingerprints)
	if len(introduced) == 0 {
		summary := fmt.Sprintf("%s passed (%d pre-existing %s, none introduced by this change)",
			s.name, len(preexisting), plural(len(preexisting)))
		return s.pass(summary, append(warnings, preexistingNote(preexisting, baseSHA)))
	}
	return s.fail(introduced, preexisting, warnings, "delta")
}

// baseFingerprints returns every fingerprint the scanner reports for the base
// commit, scanning it if the answer is not already cached.
//
// The cache matters more than it looks: a repo with a standing backlog fails
// the naive path on every push, and re-scanning the same unchanged base commit
// each time would double the gate's cost precisely for the repos that this
// change is meant to unblock. The key covers the base commit, the exact command
// and the scanner version, so any of the three changing re-scans.
func (s SecurityScanStep) baseFingerprints(ctx context.Context, sc application.StepContext, dir string, scan scanner.Command, baseSHA string) (map[string]bool, error) {
	key := baseCacheKey(baseSHA, scan.Raw, scanner.LocalVersion(ctx, dir, scan.Binary))
	if cached, ok := readBaseCache(ctx, dir, key); ok {
		return cached, nil
	}

	treeDir, err := os.MkdirTemp("", "warden-scan-base-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(treeDir) }()
	if err := materializeTree(ctx, dir, baseSHA, treeDir); err != nil {
		return nil, err
	}

	reportDir, err := os.MkdirTemp("", "warden-scan-base-report-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(reportDir) }()

	// The base scan's exit code is deliberately ignored: a non-zero exit there
	// just means the base commit already had findings, which is the thing being
	// measured. Only an unreadable report is a failure.
	_, _, _ = s.shell.runIn(ctx, sc, treeDir, scan.WithReportDir(reportDir))
	report, err := scanner.ReadReport(reportDir)
	if err != nil {
		return nil, err
	}
	fingerprints := report.Fingerprints()
	writeBaseCache(ctx, dir, key, fingerprints)
	return fingerprints, nil
}

// baselineDriftFindings reports a committed baseline that matches nothing in
// the current scan. It changes the diagnosis, never the verdict: the step still
// fails or passes on the findings themselves, but instead of "240 new
// criticals" the developer is told the baseline and the scanner have stopped
// agreeing, which is a five-minute fix rather than a month of red releases.
func (s SecurityScanStep) baselineDriftFindings(dir string, report scanner.Report, gatingCount int) (bool, []domain.Finding) {
	baseline, found, err := scanner.ReadBaseline(dir)
	if err != nil || !found {
		return false, nil
	}
	drifted, _ := scanner.BaselineDrift(baseline, report)
	if !drifted {
		return false, nil
	}

	return true, []domain.Finding{note(fmt.Sprintf(
		"baseline drift: not one of the %d entries in %s matches any of the %d findings this scan reported "+
			"(%d of them unwaived). That is the signature of a scanner version change renumbering its rules, "+
			"not of the tree suddenly regressing — check that the scanner here is the version CI pins, then "+
			"regenerate the baseline in the same commit as the bump",
		len(baseline.Fingerprints), scanner.BaselinePath, len(report.Findings), gatingCount))}
}

// versionDrift records that the local scanner is not the version CI pins.
type versionDrift struct {
	drifted bool
	err     string // pin discovery failed; refuse regardless, we know nothing
	binary  string
	local   string
	pin     scanner.Pin
}

// detectVersionDrift reports whether the local scanner differs from the pin. It
// does NOT decide anything — see resolveVersionDrift.
func (s SecurityScanStep) detectVersionDrift(ctx context.Context, dir, binary string, cfg domain.SecurityScanConfig) versionDrift {
	if !cfg.VersionCheckEnabled() {
		return versionDrift{}
	}
	pin, found, err := scanner.DiscoverPin(ctx, dir, binary, cfg.PinFile)
	if err != nil {
		return versionDrift{drifted: true, err: err.Error()}
	}
	if !found {
		// Nothing pins the scanner, so there is nothing to disagree with.
		return versionDrift{}
	}
	local := scanner.LocalVersion(ctx, dir, binary)
	if local == "" || scanner.SameVersion(local, pin.Version) {
		return versionDrift{}
	}

	return versionDrift{drifted: true, binary: binary, local: local, pin: pin}
}

// resolveVersionDrift decides what a version difference means, using the only
// evidence that settles it: whether the committed baseline still matches.
//
// # WHY THIS IS NOT A PRE-SCAN REFUSAL ANY MORE
//
// It used to refuse the moment the version strings differed, before scanning.
// The reasoning was sound — a scanner that renumbers rules between releases
// invalidates every baseline fingerprint, and discovering that at a release tag
// is expensive. But the check asserted a PROXY, and the proxy is wrong far more
// often than it is right: releases mostly do not renumber rules, so the
// baseline usually keeps matching perfectly across versions.
//
// Measured on one repo, same tree and same committed baseline:
//
//	nox 1.17.0 -> 0 findings, 969 suppressed
//	nox 1.20.0 -> 0 findings, 969 suppressed
//	nox 1.22.0 -> 0 findings, 969 suppressed
//
// Identical. Yet every push was refused, and the only ways past were to
// hand-install a matching binary or to bump the pin — which does not hold
// either, because a brew-installed scanner auto-upgrades and drifts again
// within hours. A gate that fails on something the developer cannot keep
// stable, for a harm that is not occurring, is a gate people learn to bypass
// with --no-verify. That disables the tests and the security scan too, so the
// check makes the tree less safe than not having it.
//
// So: scan first, then judge on the outcome. A version difference with a
// baseline that still matches is a NOTE. A version difference that HAS broken
// the baseline is a refusal — and now it can say so as fact rather than
// suspicion. The expensive-at-release-tag case is still caught, because that
// case is precisely the one where the baseline stops matching.
func (s SecurityScanStep) resolveVersionDrift(v versionDrift, baselineDrifted bool) (domain.StepResult, bool) {
	switch {
	case v.err != "":
		return s.refusal(v.err), true
	case !v.drifted || !baselineDrifted:
		return domain.StepResult{}, false
	}

	return s.refusal(fmt.Sprintf(
		"%s here is %s but %s pins %s=%s, AND the committed %s now matches nothing this scan reported. That is the "+
			"combination the pin exists to prevent: %s renumbered its rule IDs, so every accepted entry silently "+
			"stopped matching and the gate is about to report the whole backlog as new. Install %s %s, or bump the "+
			"pin and regenerate the baseline in the same commit (set security_scan.version_check: false to skip).",
		v.binary, v.local, v.pin.Source, v.pin.Key, v.pin.Version, scanner.BaselinePath,
		v.binary, v.binary, v.pin.Version)), true
}

// toleratedMessage explains a version difference that did no harm.
func (v versionDrift) toleratedMessage() string {
	return fmt.Sprintf(
		"%s here is %s but %s pins %s=%s. Allowed: the committed %s still matches this scan, so the rule "+
			"fingerprints did not move between these versions. Worth aligning anyway — the next release might "+
			"renumber, and then this becomes a refusal.",
		v.binary, v.local, v.pin.Source, v.pin.Key, v.pin.Version, scanner.BaselinePath)
}

func (s SecurityScanStep) refusal(msg string) domain.StepResult {
	return domain.StepResult{
		Step:     s.name,
		Status:   domain.StepFail,
		Findings: []domain.Finding{{Severity: domain.SeverityHigh, Message: msg}},
		Summary:  string(s.name) + ": scanner version drift",
	}
}

func (s SecurityScanStep) pass(summary string, warnings []domain.Finding) domain.StepResult {
	return domain.StepResult{Step: s.name, Status: domain.StepPass, Findings: compact(warnings), Summary: summary}
}

// fail builds the blocking result. blocking is what the developer must deal
// with; ignored is what was inherited and is reported only as a count, so the
// output leads with the actionable set instead of burying it.
func (s SecurityScanStep) fail(blocking, ignored []scanner.Finding, warnings []domain.Finding, mode string) domain.StepResult {
	findings := make([]domain.Finding, 0, len(blocking)+len(warnings))
	for _, f := range blocking {
		findings = append(findings, domain.Finding{
			Severity: domain.SeverityHigh,
			Message:  f.String(),
			File:     f.File,
			Line:     f.Line,
		})
	}
	findings = append(findings, compact(warnings)...)

	summary := fmt.Sprintf("%s failed: %d %s", s.name, len(blocking), plural(len(blocking)))
	if mode == "delta" {
		summary = fmt.Sprintf("%s failed: %d new %s introduced by this change", s.name, len(blocking), plural(len(blocking)))
		if len(ignored) > 0 {
			summary += fmt.Sprintf(" (%d pre-existing not counted)", len(ignored))
		}
	}
	return domain.StepResult{Step: s.name, Status: domain.StepFail, Findings: findings, Summary: summary}
}

// preexistingNote reports the inherited backlog as one counted line rather than
// N blocking findings. It is medium severity on purpose: a high-severity
// finding makes the push gate demand human approval, and turning "you inherited
// 71 old findings" into an approval prompt would rebuild the wall this change
// removes.
func preexistingNote(preexisting []scanner.Finding, baseSHA string) domain.Finding {
	if len(preexisting) == 0 {
		return domain.Finding{}
	}
	return note(fmt.Sprintf(
		"%d pre-existing unwaived %s in this tree, already present at %s and not introduced by this change. "+
			"They do not block the push; run the scanner directly to list them, or set security_scan.mode: total to gate on them.",
		len(preexisting), plural(len(preexisting)), short(baseSHA)))
}

// note is an advisory finding: information the developer should see that must
// not, by itself, fail a run or trigger the approval gate.
func note(msg string) domain.Finding {
	return domain.Finding{Severity: domain.SeverityMedium, Message: msg}
}

func compact(findings []domain.Finding) []domain.Finding {
	out := make([]domain.Finding, 0, len(findings))
	for _, f := range findings {
		if f.Message != "" {
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// stepDir is the worktree this step runs in, honoring per-step isolation.
func stepDir(sc application.StepContext, name domain.StepName) string {
	if sc.WorktreeFor != nil {
		if dir := sc.WorktreeFor(name); dir != "" {
			return dir
		}
	}
	return sc.WorktreeDir
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// plural returns the right noun for a finding count. Every call site in this
// package counts findings, so the words are fixed rather than parameters that
// only ever receive one value.
func plural(n int) string {
	if n == 1 {
		return "finding"
	}
	return "findings"
}

// baseCacheKey identifies a base scan by everything that could change its
// result.
func baseCacheKey(baseSHA, command, scannerVersion string) string {
	sum := sha256.Sum256([]byte(baseSHA + "\x00" + command + "\x00" + scannerVersion))
	return hex.EncodeToString(sum[:])
}

// baseCacheDir is where base-scan results live: under the repo's git dir, so
// they are per-clone, never committed, and swept away with the clone. Empty
// when warden cannot locate it, which disables the cache rather than failing.
func baseCacheDir(ctx context.Context, dir string) string {
	out, err := gitOut(ctx, dir, "rev-parse", "--git-common-dir")
	if err != nil {
		return ""
	}
	gitDir := strings.TrimSpace(out)
	if gitDir == "" {
		return ""
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(dir, gitDir)
	}
	return filepath.Join(gitDir, "warden", "scan-base")
}

// readBaseCache returns a cached base fingerprint set. Every failure is a miss:
// a stale or unreadable cache must cost a re-scan, never a wrong answer.
func readBaseCache(ctx context.Context, dir, key string) (map[string]bool, bool) {
	cacheDir := baseCacheDir(ctx, dir)
	if cacheDir == "" {
		return nil, false
	}
	data, err := os.ReadFile(filepath.Join(cacheDir, key+".json")) //nolint:gosec // key is a hex digest warden computed
	if err != nil {
		return nil, false
	}
	var fingerprints []string
	if err := json.Unmarshal(data, &fingerprints); err != nil {
		return nil, false
	}
	set := make(map[string]bool, len(fingerprints))
	for _, fp := range fingerprints {
		set[fp] = true
	}
	return set, true
}

// writeBaseCache stores a base fingerprint set, best-effort.
func writeBaseCache(ctx context.Context, dir, key string, fingerprints map[string]bool) {
	cacheDir := baseCacheDir(ctx, dir)
	if cacheDir == "" {
		return
	}
	if err := os.MkdirAll(cacheDir, 0o750); err != nil {
		return
	}
	list := make([]string, 0, len(fingerprints))
	for fp := range fingerprints {
		list = append(list, fp)
	}
	data, err := json.Marshal(list)
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(cacheDir, key+".json"), data, 0o600)
}
