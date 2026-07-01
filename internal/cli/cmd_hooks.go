package cli

import (
	"fmt"
	"io"

	"go.klarlabs.de/warden/internal/domain"
)

// cmdHooks handles `warden hooks enable|disable <hook>`.
func cmdHooks(args []string, stdout, stderr io.Writer) int {
	if len(args) != 2 {
		fmt.Fprintln(stderr, "usage: warden hooks enable|disable <pre-commit|pre-push>")
		return 2
	}
	action := args[0]
	hook, err := domain.ParseHook(args[1])
	if err != nil {
		return fail(stderr, err)
	}

	var enabled bool
	switch action {
	case "enable":
		enabled = true
	case "disable":
		enabled = false
	default:
		fmt.Fprintf(stderr, "warden: unknown action %q (want enable or disable)\n", action)
		return 2
	}

	svc, err := newService(autoApprover{})
	if err != nil {
		return fail(stderr, err)
	}
	if err := svc.SetHook(hook, enabled); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "warden: %s %sd\n", hook, action)
	return 0
}
