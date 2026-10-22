package steps

import (
	"bytes"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/application"
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

func TestProbeEvidenceExcludesStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires POSIX shell")
	}
	for _, live := range []bool{false, true} {
		var stdout bytes.Buffer
		var lines []string
		sc := application.StepContext{}
		if live {
			sc.OnOutput = func(line string) { lines = append(lines, line) }
		}
		// Deliberately interleave stderr in the middle of a stdout probe, as an
		// engine's two output pumps may do even for an earlier complete report.
		script := "printf 'begin\\nroutes=1\\n'; printf 'routes=0\\n' >&2; printf 'end\\nartifact\\n'"
		out, err := runCapturedStdout(exec.Command("/bin/sh", "-c", script), sc, &stdout, func(string) {})
		if err != nil {
			t.Fatal(err)
		}
		if live && (strings.Contains(strings.Join(lines, "\n"), "routes=1") || !strings.Contains(strings.Join(lines, "\n"), "routes=0")) {
			t.Fatalf("stderr was filtered with the probe: %q", lines)
		}
		_, observed := extractObservation(stdout.Bytes(), "begin", "end")
		if observed == nil || observed.Routes == nil || *observed.Routes != 1 {
			t.Fatalf("stderr changed evidence: %+v", observed)
		}
		if !strings.Contains(string(out), "routes=0") || !strings.Contains(string(out), "artifact") {
			t.Fatalf("lost command output: %q", out)
		}
	}
}

func TestProbeCaptureBoundedAndDiagnosticsPreserved(t *testing.T) {
	var capture probeCapture
	data := strings.Repeat("x", 1<<20)
	n, err := capture.Write([]byte(data))
	if err != nil || n != len(data) || capture.Len() > maxObservationBytes+1024 {
		t.Fatal("stdout copy unbounded or short write")
	}
	stdout := []byte("begin\nroutes=1\nend\nartifact\n")
	combined := []byte("begin\nroutes=1\nimportant error\nend\nartifact\n")
	if got := stripObservationFrame(combined, stdout, "begin", "end"); !bytes.Equal(got, combined) {
		t.Fatal("stderr diagnostic stripped")
	}
	if got := stripObservationFrame(stdout, stdout, "begin", "end"); string(got) != "artifact\n" {
		t.Fatalf("unstripped frame: %q", got)
	}
}
