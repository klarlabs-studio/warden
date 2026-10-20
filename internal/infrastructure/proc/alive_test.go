package proc

import (
	"os"
	"os/exec"
	"runtime"
	"testing"
)

func TestAlive(t *testing.T) {
	if !Alive(os.Getpid()) {
		t.Fatal("this process reads as gone")
	}
	if Alive(0) || Alive(-1) {
		t.Fatal("a non-positive pid reads as alive")
	}
	name, args := "sh", []string{"-c", "exit 0"}
	if runtime.GOOS == "windows" {
		name, args = "cmd", []string{"/c", "exit 0"}
	}
	cmd := exec.Command(name, args...)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if Alive(cmd.Process.Pid) {
		t.Fatalf("exited and reaped pid %d reads as alive", cmd.Process.Pid)
	}
}
