// Package cli is Warden's command-line delivery surface. Run dispatches
// subcommands over the shared service facade; the hook shims installed by
// `warden init` invoke `warden run <hook>` here.
package cli

import (
	"fmt"
	"io"
)

// Version is the Warden version, overridable at build time via -ldflags.
var Version = "0.9.0"

// Run parses args (including argv[0]) and dispatches a subcommand, returning a
// process exit code. stdout/stderr are injected for testability.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		return cmdHelp(stdout)
	}
	cmd, rest := args[1], args[2:]

	switch cmd {
	case "status":
		return cmdStatus(stdout, stderr)
	case "init":
		return cmdInit(rest, stdout, stderr)
	case "import":
		return cmdImport(rest, stdout, stderr)
	case "audit":
		return cmdAudit(rest, stdout, stderr)
	case "hooks":
		return cmdHooks(rest, stdout, stderr)
	case "run":
		return cmdRun(rest, stdout, stderr)
	case "policy":
		return cmdPolicy(rest, stdout, stderr)
	case "steps":
		return cmdSteps(rest, stdout, stderr)
	case "doctor":
		return cmdDoctor(rest, stdout, stderr)
	case "ci":
		return cmdCI(rest, stdout, stderr)
	case "verify":
		return cmdVerify(rest, stdout, stderr)
	case "key":
		return cmdKey(rest, stdout, stderr)
	case "why":
		return cmdWhy(rest, stdout, stderr)
	case "recipes":
		return cmdRecipes(rest, stdout, stderr)
	case "watch":
		return cmdWatch(rest, stdout, stderr)
	case "attach":
		return cmdAttach(rest, stdout, stderr)
	case "axi":
		return cmdAxi(rest, stdout, stderr)
	case "mcp":
		return cmdMCP(rest, stdout, stderr)
	case "help", "-h", "--help":
		return cmdHelp(stdout)
	case "version", "--version":
		fmt.Fprintln(stdout, "warden "+Version)
		return 0
	default:
		fmt.Fprintf(stderr, "warden: unknown command %q\n", cmd)
		return 2
	}
}

func cmdHelp(w io.Writer) int {
	fmt.Fprint(w, `warden — configurable git commit/push gate

Usage:
  warden status                               show gate state, armed hooks, policy
  warden init [--hooks=pre-commit,pre-push]   initialize gate, install hooks
  warden import [--write]                     generate .warden.yaml from existing CI/Makefile/scripts
  warden audit [--branch b] [--format json|md] export a commit-provenance report
  warden hooks enable|disable <hook>          change hook selection
  warden run <pre-commit|pre-push>            run the gate (invoked by hooks)
  warden policy explain [--hook h] [--branch b] [--paths glob,...] [--chart]
  warden steps list                           list built-in + custom steps
  warden doctor [--branch b]                  audit provenance since adoption
  warden ci [--branch b] [--wait]             report CI status for the branch's PR
  warden verify [--commit c] [--key fp] [--quiet]  exit 0 if the commit is warden-validated (CI skip)
  warden verify --range base..head [--require-signed] [--key fp] [--json]  gate every commit in a range
  warden key show                             print this machine's provenance signing key
  warden why [commit]                         explain what the gate did for a commit (from its note)
  warden recipes [name]                        list / print paste-able check recipes (gitleaks, semgrep, …)
  warden watch                                 re-run the fast checks on save (dev feedback loop)
  warden attach                                watch a running gate live from another terminal
  warden axi <verb>                           agent surface (TOON output)
  warden mcp serve                            MCP server over stdio
  warden version
`)
	return 0
}

// fail prints an error to stderr and returns a non-zero code.
func fail(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "warden: %v\n", err)
	return 1
}
