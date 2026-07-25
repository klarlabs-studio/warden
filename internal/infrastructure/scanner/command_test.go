package scanner

import (
	"strings"
	"testing"
)

func TestParseCommand_Recognized(t *testing.T) {
	cases := []struct {
		name          string
		raw           string
		wantThreshold string
		wantPath      string
	}{
		{"the command warden's own init writes", "nox scan . -severity-threshold high", "high", "."},
		{"no flags", "nox scan .", "", "."},
		{"inline flag value", "nox scan . -severity-threshold=critical", "critical", "."},
		{"double dash", "nox scan . --severity-threshold high", "high", "."},
		{"a subdirectory target", "nox scan services/api -severity-threshold high", "high", "services/api"},
		{"an absolute path to the binary", "/opt/homebrew/bin/nox scan .", "", "."},
		{"flags warden does not model are carried through", "nox scan . -offline -tracked-only -severity-threshold high", "high", "."},
		{"a value flag warden does model does not become the path", "nox scan . -rules ./rules.yaml", "", "."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ParseCommand(c.raw)
			if !ok {
				t.Fatalf("ParseCommand(%q) was not recognized", c.raw)
			}
			if got.Threshold != c.wantThreshold {
				t.Errorf("threshold = %q, want %q", got.Threshold, c.wantThreshold)
			}
			if got.Path != c.wantPath {
				t.Errorf("path = %q, want %q", got.Path, c.wantPath)
			}
		})
	}
}

func TestParseCommand_Declined(t *testing.T) {
	// Everything here must keep the old run-verbatim, gate-on-exit-code
	// behavior. Warden rewriting a command it only half understands is worse
	// than not rewriting it at all.
	cases := []struct {
		name, raw string
	}{
		{"empty", ""},
		{"another scanner", "npm audit"},
		{"a make target", "make audit"},
		{"not the scan subcommand", "nox diff -base main"},
		{"a pipeline", "nox scan . | tee scan.log"},
		{"a conjunction", "nox scan . && echo ok"},
		{"a subshell", "nox scan $(pwd)"},
		{"the command already directs its output", "nox scan . -output reports"},
		{"the command already picks a format", "nox scan . -format sarif"},
		{"an absolute scan path", "nox scan /etc"},
		{"a scan path escaping the tree", "nox scan ../other"},
		{"two scan paths", "nox scan . src"},
		{"a dangling value flag", "nox scan . -severity-threshold"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, ok := ParseCommand(c.raw); ok {
				t.Errorf("ParseCommand(%q) was recognized; warden would rewrite a command it does not fully understand", c.raw)
			}
		})
	}
}

func TestCommand_WithReportDir(t *testing.T) {
	cmd, ok := ParseCommand("nox scan . -severity-threshold high")
	if !ok {
		t.Fatal("expected the command to be recognized")
	}
	got := cmd.WithReportDir("/tmp/warden scan-1")
	if !strings.HasPrefix(got, "nox scan . -severity-threshold high ") {
		t.Errorf("WithReportDir dropped the original command: %q", got)
	}
	if !strings.Contains(got, "-format json") {
		t.Errorf("WithReportDir = %q, want it to request JSON", got)
	}
	// The dir is quoted so a temp path containing a space cannot split into two
	// arguments and send the report somewhere unexpected.
	if !strings.Contains(got, "'/tmp/warden scan-1'") {
		t.Errorf("WithReportDir = %q, want the output dir quoted", got)
	}
}
