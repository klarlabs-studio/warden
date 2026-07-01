package cli

import (
	"fmt"
	"io"

	"go.klarlabs.de/warden/internal/domain"
)

// cmdStatus handles the bare `warden` invocation. The spec envisions a TUI that
// attaches to an in-flight pre-push run; Warden's execution model is
// synchronous (the run completes inside the hook, there is no daemon to attach
// to), so the bare command instead reports the gate's current state: which
// hooks are armed, the resolved default policy, and the adoption point. This is
// the honest analogue given the architecture — a status view, not a live attach.
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
	fmt.Fprintln(stdout, "hooks:")
	for _, h := range domain.AllHooks {
		state := "not installed"
		if installed[h] {
			state = "armed"
		}
		fmt.Fprintf(stdout, "  %-11s %s\n", h, state)
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
	}
	fmt.Fprintln(stdout, "\nrun `warden policy explain` for the fully resolved policy.")
	return 0
}
