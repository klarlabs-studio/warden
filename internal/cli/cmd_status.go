package cli

import (
	"fmt"
	"io"
	"strings"

	"go.klarlabs.de/warden/internal/domain"
)

// cmdStatus handles the bare `warden` invocation. The spec envisions a TUI that
// attaches to an in-flight pre-push run; Warden's execution model is
// synchronous (the run completes inside the hook, there is no daemon to attach
// to), so the bare command instead reports the gate's current state: which
// hooks are armed, the resolved default policy, and the adoption point. This is
// the honest analog given the architecture — a status view, not a live attach.
func cmdStatus(stdout, stderr io.Writer) int {
	svc, err := newService(autoApprover{})
	if err != nil {
		// Outside a repo (or before init) fall back to help rather than error.
		return cmdHelp(stdout)
	}

	fmt.Fprintf(stdout, "warden %s — %s\n\n", Version, svc.Repo().Dir)

	installed, err := svc.InstalledHooks()
	if err != nil {
		return fail(stderr, err)
	}
	pins, _ := svc.HookPins() // diagnostic only; a nil map just omits the pin
	fmt.Fprintln(stdout, "hooks:")
	for _, h := range domain.AllHooks {
		state := "not installed"
		if installed[h] {
			state = "armed"
			if v := pins[h]; v != "" {
				state += " (pinned " + v + ")"
			}
		}
		fmt.Fprintf(stdout, "  %-11s %s\n", h, state)
	}
	if line := pinSkewLine(pins, Version); line != "" {
		fmt.Fprintf(stdout, "\n%s\n", line)
	}

	if adoption, err := svc.Repo().ReadAdoption(); err == nil && adoption != "" {
		fmt.Fprintf(stdout, "\nadoption point: %s\n", short(adoption))
	} else {
		fmt.Fprintln(stdout, "\nnot initialized — run `warden init`")
	}

	pre, push, err := svc.StepsList()
	if err == nil {
		fmt.Fprintln(stdout, "\nsteps:")
		printSteps(stdout, "  pre-commit", pre)
		printSteps(stdout, "  pre-push", push)
		if tip := watchTip(installed, pre, push); tip != "" {
			fmt.Fprintf(stdout, "\n%s\n", tip)
		}
	}
	fmt.Fprintln(stdout, "\nrun `warden policy explain` for the fully resolved policy.")
	return 0
}

// pinSkewLine reports hooks whose pinned version differs from the binary that
// actually runs them. The shims prefer a warden on PATH, so the pin only ever
// governs a machine with no global install — meaning a pin/binary mismatch is
// the normal case for most developers and is silently in force at every commit.
// Naming it here is the honest counterpart to that design: the pin is not a
// lock, and `warden status` should say so before a version-dependent
// disagreement has to be reverse-engineered. Pure so the wording is testable.
func pinSkewLine(pins map[domain.Hook]string, running string) string {
	var skewed []string
	for _, h := range domain.AllHooks {
		if v := pins[h]; v != "" && v != running {
			skewed = append(skewed, fmt.Sprintf("%s pins %s", h, v))
		}
	}
	if len(skewed) == 0 {
		return ""
	}
	return fmt.Sprintf("note: %s, but %s is what runs (hooks prefer a warden on PATH).\n      re-pin with `warden hooks enable <hook>`.",
		strings.Join(skewed, "; "), running)
}

// watchTip surfaces `warden watch` exactly where it earns its keep: a split
// policy that keeps pre-commit fast by deferring checks to pre-push leaves a gap
// between "commit is green" and "the suite is green", and watch is the tool
// built to close it. It stays silent unless pre-commit is the armed hook doing
// the deferring — with no pre-commit shim there is nothing to defer *from*, and
// with nothing deferred the tip is noise. Pure so the condition is testable.
func watchTip(installed map[domain.Hook]bool, pre, push []domain.StepName) string {
	if !installed[domain.PreCommit] {
		return ""
	}
	deferred := domain.DeferredSteps(pre, push)
	if len(deferred) == 0 {
		return ""
	}
	return fmt.Sprintf("tip: `warden watch` re-runs these on save, so deferred steps (%s) don't wait for push.",
		domain.JoinSteps(deferred))
}
