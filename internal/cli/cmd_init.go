package cli

import (
	"flag"
	"fmt"
	"io"

	"go.klarlabs.de/warden/internal/domain"
)

// cmdInit handles `warden init [--hooks=...]`, installing the selected hooks,
// writing a starter config, and recording the adoption point.
func cmdInit(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	hooksFlag := fs.String("hooks", "", "comma-separated hooks to install (default: pre-commit,pre-push)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	selected, err := parseHooksFlag(*hooksFlag)
	if err != nil {
		return fail(stderr, err)
	}

	svc, err := newService(autoApprover{})
	if err != nil {
		return fail(stderr, err)
	}
	lang, err := svc.Init(selected)
	if err != nil {
		return fail(stderr, err)
	}

	fmt.Fprintf(stdout, "warden initialized. installed hooks:")
	for _, h := range selected {
		fmt.Fprintf(stdout, " %s", h)
	}
	fmt.Fprintln(stdout)
	if lang != domain.LangUnknown {
		fmt.Fprintf(stdout, "detected %s — pre-filled lint/test commands in .warden.yaml (adjust as needed).\n", lang)
	}
	fmt.Fprintln(stdout, "adoption point recorded at current HEAD; edit .warden.yaml to configure policy.")
	return 0
}
