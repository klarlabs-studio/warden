package cli

import (
	"bytes"
	"strings"
	"testing"
)

// TestCmdMCP_Usage covers the argument guard: `warden mcp` needs the `serve`
// subcommand, and anything else is a usage error (exit 2) rather than starting
// the stdio server.
func TestCmdMCP_Usage(t *testing.T) {
	for _, args := range [][]string{nil, {}, {"bogus"}} {
		var out, errb bytes.Buffer
		if rc := cmdMCP(args, &out, &errb); rc != 2 {
			t.Errorf("cmdMCP(%q): rc=%d, want 2", args, rc)
		}
		if !strings.Contains(errb.String(), "usage: warden mcp serve") {
			t.Errorf("cmdMCP(%q): missing usage message, got %q", args, errb.String())
		}
	}
}

// TestCmdAxi_ErrorPaths exercises the axi dispatcher's usage/error branches
// (no server, no command execution): usage, unknown verb, bad flags, and an
// invalid --hook that fails ParseHook after the trust gate.
func TestCmdAxi_ErrorPaths(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"no args", nil},
		{"unknown verb", []string{"frobnicate"}},
		{"policy-explain bad flag", []string{"policy-explain", "--nope"}},
		{"policy-explain bad hook", []string{"policy-explain", "--hook", "bogus"}},
		{"run-trigger bad flag", []string{"run-trigger", "--nope"}},
	}
	for _, c := range cases {
		var out, errb bytes.Buffer
		if rc := cmdAxi(c.args, &out, &errb); rc == 0 {
			t.Errorf("cmdAxi(%s): rc=0, want non-zero error", c.name)
		}
	}

	// run-trigger with an invalid hook, past the trust gate (env opt-in): it must
	// fail at hook parsing, never reaching command execution.
	t.Setenv("WARDEN_MCP_ALLOW_RUN", "1")
	var out, errb bytes.Buffer
	if rc := cmdAxi([]string{"run-trigger", "--hook", "bogus"}, &out, &errb); rc == 0 {
		t.Error("run-trigger with a bogus hook must fail, not run")
	}
}

// TestCmdAxi_RunTriggerRefusedWithoutTrust confirms run-trigger refuses to
// execute repo commands unless explicitly trusted.
func TestCmdAxi_RunTriggerRefusedWithoutTrust(t *testing.T) {
	t.Setenv("WARDEN_MCP_ALLOW_RUN", "")
	var out, errb bytes.Buffer
	if rc := cmdAxi([]string{"run-trigger"}, &out, &errb); rc == 0 {
		t.Error("run-trigger without trust must refuse (non-zero)")
	}
	if !strings.Contains(errb.String(), "trust") {
		t.Errorf("refusal should mention trust, got %q", errb.String())
	}
}
