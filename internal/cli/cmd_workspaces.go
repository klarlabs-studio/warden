package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/service"
)

// maxReasonPaths bounds how many changed paths the text output lists per
// workspace; --json carries all of them.
const maxReasonPaths = 3

// cmdWorkspaces handles `warden workspaces [--affected [--base REV]] [--json]`
// (monorepo mode, preview).
//
// Without --affected it lists the workspace graph committed at HEAD. With
// --affected it selects the workspaces a change from the merge-base of HEAD
// and --base (default: the remote's default branch) affects, with the reason
// for each. Both read committed git objects only. The command is read-only:
// it writes no note, runs no step, and never runs a package manager.
func cmdWorkspaces(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("workspaces", flag.ContinueOnError)
	fs.SetOutput(stderr)
	affected := fs.Bool("affected", false, "select the workspaces a change affects instead of listing all of them")
	base := fs.String("base", "", "with --affected: the revision to measure from (default: the remote's default branch)")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if rejectExtraArgs(fs, stderr, "workspaces", "") {
		return 2
	}
	if *base != "" && !*affected {
		_, _ = fmt.Fprintln(stderr, "warden: workspaces: --base needs --affected")
		return 2
	}
	svc, err := newService(autoApprover{})
	if err != nil {
		return fail(stderr, err)
	}
	if !*affected {
		list, err := svc.Workspaces("HEAD")
		if err != nil {
			return fail(stderr, err)
		}
		if *asJSON {
			return writeJSON(stdout, stderr, list)
		}
		renderWorkspaceList(stdout, list)
		return 0
	}
	if *base == "" {
		if *base, err = svc.DefaultAffectedBase(); err != nil {
			return fail(stderr, err)
		}
	}
	res, err := svc.AffectedWorkspaces(*base)
	if err != nil {
		return fail(stderr, err)
	}
	if *asJSON {
		return writeJSON(stdout, stderr, res)
	}
	renderAffected(stdout, res)
	return 0
}

func writeJSON(stdout, stderr io.Writer, v any) int {
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fail(stderr, err)
	}
	return 0
}

func renderWorkspaceList(w io.Writer, list service.WorkspaceList) {
	if len(list.Workspaces) == 0 {
		_, _ = fmt.Fprintf(w, "no workspaces at %s: no pnpm-workspace.yaml, package.json \"workspaces\", go.work or Cargo.toml [workspace] declares any\n", short(list.Commit))
		return
	}
	_, _ = fmt.Fprintf(w, "%d workspace(s) at %s (graph %s)\n", len(list.Workspaces), short(list.Commit), list.GraphDigest)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "  PATH\tNAME\tECOSYSTEM\tDEPENDS ON")
	for _, ws := range list.Workspaces {
		deps := "-"
		if len(ws.Deps) > 0 {
			deps = strings.Join(ws.Deps, ", ")
		}
		_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", ws.Path, ws.Name, ws.Ecosystem, deps)
	}
	_ = tw.Flush()
}

func renderAffected(w io.Writer, res service.AffectedWorkspaces) {
	sel := res.Selection
	_, _ = fmt.Fprintf(w, "measured from %s (merge-base of HEAD and %s) to %s; %d changed path(s)\n",
		short(res.Anchor), res.Base, short(res.Head), len(sel.Changed))
	if sel.Kind == domain.SelectionFull {
		_, _ = fmt.Fprintf(w, "full: all %d workspace(s), because %s\n", res.HeadWorkspaces, forcedFullReason(sel.Full))
	} else {
		_, _ = fmt.Fprintf(w, "affected: %d of %d workspace(s)\n", len(sel.Workspaces), res.HeadWorkspaces)
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, ws := range sel.Workspaces {
			_, _ = fmt.Fprintf(tw, "  %s\t%s\n", ws.Path, selectionReasons(ws.Reasons))
		}
		_ = tw.Flush()
	}
	if len(sel.Removed) > 0 {
		_, _ = fmt.Fprintf(w, "removed: %s (nothing runs for them; their former dependents are selected)\n", strings.Join(sel.Removed, ", "))
	}
	_, _ = fmt.Fprintf(w, "selection %s\n", res.SelectionDigest)
}

// forcedFullReason says in words which rule made a selection full.
func forcedFullReason(f *domain.ForcedFull) string {
	if f == nil {
		return "the selection is full"
	}
	switch f.Rule {
	case domain.FullRuleConfiguration:
		if f.Path != "" {
			return f.Path + " changed"
		}
		return "the effective configuration differs from the anchor's, or the anchor's could not be read"
	case domain.FullRuleInvalidPath:
		return fmt.Sprintf("the changed path %q cannot be attributed to a workspace", f.Path)
	case domain.FullRuleRootManifest:
		return "the root manifest " + f.Path + " changed"
	case domain.FullRuleLockfile:
		return "the root lockfile " + f.Path + " changed"
	case domain.FullRuleGlobal:
		return fmt.Sprintf("%s matches the global pattern %q", f.Path, f.Pattern)
	case domain.FullRuleUnowned:
		return f.Path + " belongs to no workspace"
	}
	return string(f.Rule)
}

func selectionReasons(reasons []domain.SelectionReason) string {
	parts := make([]string, 0, len(reasons))
	for _, r := range reasons {
		switch r.Kind {
		case domain.ReasonChanged:
			parts = append(parts, "changed: "+abbreviate(r.Paths))
		case domain.ReasonDependent:
			parts = append(parts, "depends on: "+strings.Join(r.Via, ", "))
		default:
			parts = append(parts, string(r.Kind))
		}
	}
	return strings.Join(parts, "; ")
}

func abbreviate(paths []string) string {
	if len(paths) <= maxReasonPaths {
		return strings.Join(paths, ", ")
	}
	return fmt.Sprintf("%s (+%d more)", strings.Join(paths[:maxReasonPaths], ", "), len(paths)-maxReasonPaths)
}
