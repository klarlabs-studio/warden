package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/infrastructure/attach"
)

// A background `warden watch --staged` yields to a gate run it can see on the
// attach socket. Only pre-push used to publish there, so the run it overlaps
// most, a pre-commit, was invisible and the two competed for the container
// engine. The gate's own check proves the socket is live while it runs.
func TestPreCommitRunPublishesOnTheAttachSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell check")
	}
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	dir := repoWithConfig(t, "commands: { probe: 'true' }\n")
	svc, err := newService(autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	gitDir, err := svc.GitDir()
	if err != nil {
		t.Fatal(err)
	}
	socket := attach.SocketPath(gitDir)
	cfg := "steps: { pre_commit: [probe] }\ncommands: { probe: \"test -S '" + socket + "'\" }\nrules: []\nnotify: false\n"
	if err := os.WriteFile(filepath.Join(dir, ".warden.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	stageFile(t, dir, "probe-input", "probe\n")
	var out, errb bytes.Buffer
	if code := cmdRun([]string{"pre-commit"}, &out, &errb); code != 0 {
		t.Fatalf("pre-commit did not publish on %s: exit %d\n%s\n%s", socket, code, out.String(), errb.String())
	}
	if attach.Live(gitDir) {
		t.Fatal("the socket outlived the run")
	}
	if !strings.Contains(out.String(), "probe") {
		t.Fatalf("the probe step did not run:\n%s", out.String())
	}
}
