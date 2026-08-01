package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/service"
)

// cmdFleet handles `warden fleet status`, a rollup of gate coverage across many
// repositories.
//
// It exists to measure the failure mode warden's own docs identify as the one
// that makes a gate worthless: being routinely routed around. A gate that is
// bypassed protects nothing AND removes the signal that it ever ran, so the
// bypass rate is the number that says whether adoption is real. Nothing measured
// it — `doctor` answers it one repo at a time, which is exactly the granularity
// at which a fleet-wide drift is invisible.
func cmdFleet(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 || args[0] != "status" {
		_, _ = fmt.Fprintln(stderr, "usage: warden fleet status [--root DIR] [--json] [PATH...]")
		return 2
	}
	fs := flag.NewFlagSet("fleet status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", "", "scan this directory's immediate children for warden-adopted repos")
	asJSON := fs.Bool("json", false, "emit the rollup as JSON")
	branch := fs.String("branch", "", "branch to audit in each repo (default: each repo's current)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	paths, err := fleetPaths(*root, fs.Args())
	if err != nil {
		return fail(stderr, err)
	}
	if len(paths) == 0 {
		_, _ = fmt.Fprintln(stderr, "warden: no repositories given — pass paths, or --root DIR to scan")
		return 2
	}

	report := surveyFleet(paths, *branch)
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			return fail(stderr, err)
		}
	} else {
		printFleet(stdout, report)
	}
	// Non-zero when any commit was genuinely bypassed, so this composes as a CI
	// check. A recoverable squash-merge gap is NOT a bypass and must not fail
	// the command — see fleetRepo.Bypassed.
	if report.Bypassed > 0 {
		return 1
	}
	return 0
}

// fleetReport is the rollup across every surveyed repository.
type fleetReport struct {
	Repos []fleetRepo `json:"repos"`
	// Adopted counts repos that have actually adopted warden; Skipped, paths that
	// are not warden-gated repos at all. Reporting them apart matters: "3 of 40
	// repos are gated" is an adoption problem, and averaging the other 37 into a
	// clean-looking bypass rate would hide it.
	Adopted int `json:"adopted"`
	Skipped int `json:"skipped"`
	// Stalled counts repos carrying a .warden.yaml that were never adopted — the
	// actionable subset of Skipped, since someone already intended to gate them.
	Stalled int `json:"stalled"`
	// Commits is the total examined since adoption across adopted repos.
	Commits int `json:"commits"`
	// Verified carries a note; Bypassed has none and none is recoverable;
	// Reattestable has none but a tree-identical validated commit exists.
	Verified     int `json:"verified"`
	Bypassed     int `json:"bypassed"`
	Reattestable int `json:"reattestable"`
	// BypassRate is Bypassed/Commits as a percentage, rounded to one decimal.
	BypassRate float64 `json:"bypass_rate"`
}

// fleetRepo is one repository's line in the rollup.
type fleetRepo struct {
	Path   string `json:"path"`
	Branch string `json:"branch,omitempty"`
	// Adopted is false when the path is not a warden-gated repo. The remaining
	// counts are then meaningless and are left zero.
	Adopted bool `json:"adopted"`
	// Configured is true when the repo carries a .warden.yaml. Together with
	// Adopted it separates two states a bare "not gated" would merge:
	//
	//	configured && !adopted   someone wrote the config and never ran `warden init`
	//	!configured && !adopted  the repo is simply outside the fleet
	//
	// The first is a stalled adoption with a one-command fix and someone who
	// already intended it; the second is not a problem at all. Reporting them
	// identically buries the actionable one in a list of the irrelevant.
	Configured bool `json:"configured"`
	Commits    int  `json:"commits"`
	Verified   int  `json:"verified"`
	// Bypassed counts commits with NO note and NO recoverable source — the ones
	// that really did go round the gate.
	//
	// This is deliberately NOT the same as doctor's "unverified" count. A
	// squash-merge loses the note's binding while the CONTENT was gated under the
	// pre-squash commit, and counting those as bypasses would inflate the number
	// that is supposed to trigger an intervention. Overstating the problem gets
	// the metric dismissed as noisy, which costs more than not having it.
	Bypassed     int `json:"bypassed"`
	Reattestable int `json:"reattestable"`
	// Error is set when the repo could not be surveyed at all. It never stops the
	// rollup: one unreadable checkout must not deny the answer for the rest.
	Error string `json:"error,omitempty"`
}

// BypassRate is this repo's bypassed share of commits since adoption.
func (r fleetRepo) BypassRate() float64 {
	if r.Commits == 0 {
		return 0
	}
	return round1(float64(r.Bypassed) / float64(r.Commits) * 100)
}

// fleetPaths resolves the repositories to survey from explicit paths plus an
// optional scan root.
func fleetPaths(root string, explicit []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		abs, err := filepath.Abs(p)
		if err != nil || seen[abs] {
			return
		}
		seen[abs] = true
		out = append(out, abs)
	}
	for _, p := range explicit {
		add(p)
	}
	if root != "" {
		entries, err := os.ReadDir(root)
		if err != nil {
			return nil, fmt.Errorf("scan %s: %w", root, err)
		}
		// One level only, deliberately. A recursive walk of a development
		// directory descends into node_modules, vendor trees and nested
		// worktrees, which is slow and surfaces repos nobody meant to include.
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			child := filepath.Join(root, e.Name())
			if _, err := os.Stat(filepath.Join(child, ".git")); err == nil {
				add(child)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// surveyFleet audits each repository and totals the result. A repo that cannot
// be opened or audited is recorded with its error and skipped in the totals —
// never fatal, because a rollup that dies on the first bad checkout is useless
// at exactly the scale it exists for.
func surveyFleet(paths []string, branch string) fleetReport {
	var rep fleetReport
	for _, path := range paths {
		r := surveyRepo(path, branch)
		rep.Repos = append(rep.Repos, r)
		if !r.Adopted {
			rep.Skipped++
			if r.Configured {
				rep.Stalled++
			}
			continue
		}
		rep.Adopted++
		rep.Commits += r.Commits
		rep.Verified += r.Verified
		rep.Bypassed += r.Bypassed
		rep.Reattestable += r.Reattestable
	}
	if rep.Commits > 0 {
		rep.BypassRate = round1(float64(rep.Bypassed) / float64(rep.Commits) * 100)
	}
	return rep
}

// surveyRepo audits one repository.
func surveyRepo(path, branch string) fleetRepo {
	r := fleetRepo{Path: path}
	// Read this before opening the service: a repo whose config exists but whose
	// adoption never happened must still be reported as configured, and the
	// service call below is exactly what fails in that case.
	if _, err := os.Stat(filepath.Join(path, ".warden.yaml")); err == nil {
		r.Configured = true
	}
	svc, err := service.New(path, Version, autoApprover{})
	if err != nil {
		r.Error = err.Error()
		return r
	}
	report, err := svc.Doctor(branch)
	if err != nil {
		// The common case here is "never adopted", which is not an error worth
		// shouting about — it is simply a repo outside the fleet.
		r.Error = err.Error()
		return r
	}
	r.Adopted = true
	r.Branch = report.Branch
	r.Commits = len(report.Commits)
	verified, _, _ := report.Counts()
	r.Verified = verified
	r.Reattestable = len(report.Reattestable())
	r.Bypassed = countBypassed(report)
	return r
}

// countBypassed counts commits that carry no note AND have no tree-identical
// validated commit to recover from — the ones that genuinely went round the
// gate, as opposed to the ones a squash-merge unbound.
func countBypassed(report domain.AuditReport) int {
	n := 0
	for i := range report.Commits {
		c := &report.Commits[i]
		if !c.HasNote && !c.Reattestable() {
			n++
		}
	}
	return n
}

// printFleet renders the rollup for a human. The bypass rate leads, because it
// is the number that decides whether anything needs doing.
func printFleet(w io.Writer, rep fleetReport) {
	_, _ = fmt.Fprintf(w, "%d repos gated, %d skipped", rep.Adopted, rep.Skipped)
	if rep.Stalled > 0 {
		_, _ = fmt.Fprintf(w, " (%d configured but never adopted)", rep.Stalled)
	}
	_, _ = fmt.Fprintln(w)
	if rep.Commits > 0 {
		_, _ = fmt.Fprintf(w, "%d commits since adoption: %d verified, %d bypassed (%.1f%%), %d reattestable\n\n",
			rep.Commits, rep.Verified, rep.Bypassed, rep.BypassRate, rep.Reattestable)
	} else {
		_, _ = fmt.Fprintln(w)
	}

	for i := range rep.Repos {
		r := &rep.Repos[i]
		name := filepath.Base(r.Path)
		switch {
		case !r.Adopted && r.Configured:
			_, _ = fmt.Fprintf(w, "  !  %-28s configured but never adopted — run `warden init`\n", name)
		case !r.Adopted:
			_, _ = fmt.Fprintf(w, "  –  %-28s not warden-gated\n", name)
		case r.Bypassed > 0:
			_, _ = fmt.Fprintf(w, "  ✗  %-28s %d/%d bypassed (%.1f%%)\n",
				name, r.Bypassed, r.Commits, r.BypassRate())
		case r.Reattestable > 0:
			_, _ = fmt.Fprintf(w, "  ~  %-28s %d verified, %d reattestable\n",
				name, r.Verified, r.Reattestable)
		default:
			_, _ = fmt.Fprintf(w, "  ✓  %-28s %d verified\n", name, r.Verified)
		}
	}

	if rep.Bypassed > 0 {
		_, _ = fmt.Fprintln(w, "\nA bypassed commit went round the gate (`--no-verify`, or a push from a")
		_, _ = fmt.Fprintln(w, "checkout without hooks installed). A gate that is routinely bypassed")
		_, _ = fmt.Fprintln(w, "protects nothing and removes the signal that it ever ran.")
	}
	if rep.Reattestable > 0 {
		_, _ = fmt.Fprintln(w, "\nReattestable commits were gated under a different commit id (a squash-merge")
		_, _ = fmt.Fprintln(w, "unbound the note). They are NOT bypasses; `warden reattest --all` rebinds them.")
	}
}

// round1 rounds to one decimal place, so a rate reads as 12.5 rather than
// 12.499999999999998.
func round1(f float64) float64 {
	return float64(int(f*10+0.5)) / 10
}
