package cli

import (
	"fmt"
	"io"

	"go.klarlabs.de/warden/internal/domain"
)

// cmdSteps handles `warden steps list`, grouping the configured steps by hook.
func cmdSteps(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 || args[0] != "list" {
		fmt.Fprintln(stderr, "usage: warden steps list")
		return 2
	}
	svc, err := newService(autoApprover{})
	if err != nil {
		return fail(stderr, err)
	}
	pre, push, err := svc.StepsList()
	if err != nil {
		return fail(stderr, err)
	}
	printSteps(stdout, "pre-commit", pre)
	printSteps(stdout, "pre-push", push)
	return 0
}

func printSteps(w io.Writer, hook string, steps []domain.StepName) {
	fmt.Fprintf(w, "%s:\n", hook)
	for _, s := range steps {
		kind := "custom"
		if s.IsBuiltin() {
			kind = "built-in"
		}
		fmt.Fprintf(w, "  %-14s (%s)\n", s, kind)
	}
}
