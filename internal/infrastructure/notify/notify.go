// Package notify sends a best-effort desktop notification when a long run
// finishes, so a developer who tabbed away learns the gate's verdict without
// watching the terminal. It shells out to the platform's native tool and never
// errors — a missing tool just means no notification.
package notify

import (
	"context"
	"os/exec"
	"runtime"
	"time"
)

// Send posts a desktop notification with title and body. It is best-effort and
// returns nothing: an unsupported platform or missing tool is a silent no-op.
// A short timeout keeps a wedged notifier from blocking the caller.
func Send(title, body string) {
	name, args := command(runtime.GOOS, title, body)
	if name == "" {
		return
	}
	if _, err := exec.LookPath(name); err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, name, args...).Run()
}

// command returns the notifier binary and args for goos, or ("", nil) when the
// platform has no supported tool. macOS scripts osascript; Linux uses
// notify-send; other platforms are unsupported for now.
func command(goos, title, body string) (bin string, args []string) {
	switch goos {
	case "darwin":
		script := "display notification " + quote(body) + " with title " + quote(title)
		return "osascript", []string{"-e", script}
	case "linux":
		return "notify-send", []string{title, body}
	default:
		return "", nil
	}
}

// quote wraps s in double quotes for an AppleScript string literal, escaping
// backslashes and quotes so a finding message can't break out.
func quote(s string) string {
	out := make([]rune, 0, len(s)+2)
	out = append(out, '"')
	for _, r := range s {
		if r == '"' || r == '\\' {
			out = append(out, '\\')
		}
		out = append(out, r)
	}
	out = append(out, '"')
	return string(out)
}
