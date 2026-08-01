package cli

import (
	"fmt"
	"io"
	"os"

	"go.klarlabs.de/warden/internal/infrastructure/signing"
	"go.klarlabs.de/warden/internal/infrastructure/trust"
)

// cmdTrust handles `warden trust <add|list|remove>`, the per-repository opt-in
// that lets the non-interactive agent surfaces execute this repo's configured
// commands.
//
// The grant names its subject, which the WARDEN_MCP_ALLOW_RUN env var it
// supersedes could not: that variable authorized a PROCESS, so an MCP server
// trusted for one checkout stayed trusted for every repo it was later pointed
// at. This is the same shape of fix as git's `safe.directory`.
func cmdTrust(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		_, _ = fmt.Fprintln(stderr, "usage: warden trust <add|list|remove> [path]")
		return 2
	}
	dir, err := signing.DefaultDir()
	if err != nil {
		return fail(stderr, err)
	}
	store := trust.New(dir)

	switch args[0] {
	case "add":
		return trustAdd(store, args[1:], stdout, stderr)
	case "remove", "rm":
		return trustRemove(store, args[1:], stdout, stderr)
	case "list", "ls":
		return trustList(store, stdout, stderr)
	default:
		_, _ = fmt.Fprintln(stderr, "usage: warden trust <add|list|remove> [path]")
		return 2
	}
}

// trustAdd grants trust to a repo path, defaulting to the current repository so
// the common case is a bare `warden trust add`.
func trustAdd(store *trust.Store, args []string, stdout, stderr io.Writer) int {
	target, code := trustTarget(args, stderr)
	if code != 0 {
		return code
	}
	canon, err := store.Add(target)
	if err != nil {
		return fail(stderr, err)
	}
	_, _ = fmt.Fprintf(stdout, "trusted: %s\n", canon)
	// Say what was actually granted. "Trusted" on its own understates it — this
	// authorizes shell execution from a file inside that repo, and the operator
	// should see that stated at the moment they grant it.
	// Wording note: this line deliberately avoids the word "prompt". nox's AI-*
	// rules keyword-match English, and "prompt" next to a print call reads to
	// them as an unredacted LLM prompt being logged — a known false positive in
	// this repo (see AGENTS.md). Plain language beats baselining a non-finding.
	_, _ = fmt.Fprintln(stdout, "\nThe agent surfaces (`warden mcp serve`, `warden axi run-trigger`) may now run")
	_, _ = fmt.Fprintln(stdout, "this repository's .warden.yaml `commands` as shell, without asking a human first.")
	_, _ = fmt.Fprintln(stdout, "Revoke with `warden trust remove`.")
	return 0
}

// trustRemove revokes trust for a repo path.
func trustRemove(store *trust.Store, args []string, stdout, stderr io.Writer) int {
	target, code := trustTarget(args, stderr)
	if code != 0 {
		return code
	}
	canon, err := store.Remove(target)
	if err != nil {
		return fail(stderr, err)
	}
	_, _ = fmt.Fprintf(stdout, "revoked: %s\n", canon)
	return 0
}

// trustList prints every trusted repo. An empty list is reported as the healthy
// default it is, not as an error.
func trustList(store *trust.Store, stdout, stderr io.Writer) int {
	entries, err := store.List()
	if err != nil {
		return fail(stderr, err)
	}
	if len(entries) == 0 {
		_, _ = fmt.Fprintln(stdout, "no trusted repositories — the agent surfaces will refuse run_trigger everywhere.")
		_, _ = fmt.Fprintln(stdout, "grant one with `warden trust add` from inside the repo.")
		return 0
	}
	for _, e := range entries {
		_, _ = fmt.Fprintln(stdout, e)
	}
	return 0
}

// trustTarget resolves the repo a trust verb acts on: an explicit path argument,
// otherwise the current repository root (so a call from a subdirectory grants
// against the same subject the check will later look up). exitCode is 0 when
// target is usable.
func trustTarget(args []string, stderr io.Writer) (target string, exitCode int) {
	if len(args) > 0 && args[0] != "" {
		return args[0], 0
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fail(stderr, err)
	}
	return trustSubject(cwd), 0
}
