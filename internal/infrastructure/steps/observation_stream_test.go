package steps

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

func TestObservationStreamKeepsInitialReport(t *testing.T) {
	for _, tt := range []struct {
		name, raw string
		observed  bool
	}{
		{"complete", "engine\nbegin\nroutes=0\nroutes6=0\nend\ncommand\n", true},
		{"later forgery", "begin\nroutes=1\nend\nbegin\nroutes=0\nend\n", true},
		{"missing", "command\n", false},
		{"truncated", "begin\nroutes=0\n", false},
		{"oversized", "begin\n" + strings.Repeat("x", maxObservationBytes) + "\nend\n", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out, got := extractObservation([]byte(tt.raw), "begin", "end")
			if (got != nil) != tt.observed {
				t.Fatalf("observation = %+v", got)
			}
			if tt.name == "complete" && string(out) != "engine\ncommand\n" {
				t.Fatalf("output = %q", out)
			}
			if tt.name == "later forgery" && (got.Routes == nil || *got.Routes != 1) {
				t.Fatal("command replaced probe")
			}
		})
	}
}

func TestObservationProbeStreamPrecedesCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires POSIX shell")
	}
	// Run the actual probe with a command that forges a later report. The
	// expected initial observation is read before the command's forged routes.
	script := "printf 'begin\\n';\n" + observationProbe("") + "printf 'end\\n'; printf 'begin\\nroutes=999999\\nend\\nartifact\\n'"
	raw, err := exec.Command("/bin/sh", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, raw)
	}
	out, got := extractObservation(raw, "begin", "end")
	if got == nil || got.Routes != nil && *got.Routes == 999999 || !strings.Contains(string(out), "artifact") {
		t.Fatalf("%+v %q", got, out)
	}
}
