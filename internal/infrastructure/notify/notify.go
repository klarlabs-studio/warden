// Package notify sends a best-effort desktop notification when a long run
// finishes, so a developer who tabbed away learns the gate's verdict without
// watching the terminal. It shells out to the platform's native tool and never
// errors — a missing tool just means no notification.
package notify

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"time"
)

// Notification is what the user actually sees. It is structured rather than two
// opaque strings because a notification that says only "failed" is noise: the
// reader needs to know *which repo*, *which branch*, and *what to do* without
// switching to the terminal to find out.
type Notification struct {
	// Title is the verdict line, e.g. "warden: pre-push failed".
	Title string
	// Subtitle scopes the verdict, e.g. "warden · feat/gate-reporting".
	// Notifiers that have no subtitle slot fold it into the body.
	Subtitle string
	// Body is the actionable detail, e.g. "step lint failed".
	Body string
	// Urgent marks a verdict that must not be missed (a blocked push). Notifiers
	// that support urgency levels make these persist rather than auto-dismiss.
	Urgent bool
	// Group collapses successive notifications about the same repo onto one
	// another instead of stacking a column of stale verdicts. Empty means no
	// grouping.
	Group string
}

// runNotifier shells out to the platform notifier. It is a package var so tests
// can stub it — otherwise exercising Send on a machine WITH a notifier (a dev's
// macOS box) pops a real desktop notification on every `go test` run.
var runNotifier = func(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}

// lookPath is a seam for the same reason: tests must be able to decide which
// notifiers "exist" without depending on what the dev machine has installed.
var lookPath = exec.LookPath

// Send posts a desktop notification. It is best-effort and returns nothing: an
// unsupported platform or missing tool is a silent no-op. A short timeout keeps
// a wedged notifier from blocking the caller.
func Send(n Notification) {
	name, args := resolve(runtime.GOOS, n)
	if name == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = runNotifier(ctx, name, args...)
}

// Advice returns a one-line diagnostic when notifications on this machine will
// be degraded, or "" when they will work properly (or the platform supports
// none at all, where advice would be noise).
//
// It exists because the macOS fallback fails *silently and invisibly*: an
// osascript notification belongs to Script Editor, so a user who never granted
// Script Editor notification access simply never sees a verdict — nothing warns
// them, and the notification appears blocked in System Settings under an app
// they did not know was involved. A status surface naming the cause is the
// difference between "notifications are broken" and a one-line fix.
func Advice() string { return advice(runtime.GOOS) }

func advice(goos string) string {
	switch goos {
	case "darwin":
		if _, err := lookPath("terminal-notifier"); err == nil {
			return ""
		}
		if _, err := lookPath("osascript"); err != nil {
			return ""
		}
		return "notifications fall back to osascript, so macOS files them under Script Editor —\n" +
			"      they stay silent unless Script Editor has notification access, and clicking one\n" +
			"      opens an empty script. `brew install terminal-notifier` for warden's own alerts."
	case "linux":
		if _, err := lookPath("notify-send"); err == nil {
			return ""
		}
		return "no notify-send on PATH, so gate verdicts will not reach the desktop —\n" +
			"      install libnotify (e.g. `apt install libnotify-bin`)."
	default:
		return ""
	}
}

// resolve picks the best notifier actually present on this machine and builds
// its arguments, or ("", nil) when none is available.
func resolve(goos string, n Notification) (bin string, args []string) {
	for _, c := range candidates(goos, n) {
		if _, err := lookPath(c.bin); err == nil {
			return c.bin, c.args
		}
	}
	return "", nil
}

type candidate struct {
	bin  string
	args []string
}

// candidates lists the platform's notifiers in preference order.
//
// On macOS `terminal-notifier` is preferred over `osascript` for a reason that
// looks cosmetic and is not: a notification posted via osascript is *owned by
// Script Editor*. It therefore appears under Script Editor in System Settings —
// so a user who never granted Script Editor notification access sees warden's
// notifications silently blocked — and clicking it launches Script Editor with
// an empty document instead of returning to the work. terminal-notifier posts
// under its own identity, can hand the click back to the terminal warden is
// running in (see senderBundle), and supports grouping so a repo's verdicts
// replace rather than stack. osascript remains the fallback because it needs no
// install and a degraded notification beats none.
func candidates(goos string, n Notification) []candidate {
	switch goos {
	case "darwin":
		return []candidate{
			{bin: "terminal-notifier", args: terminalNotifierArgs(n)},
			{bin: "osascript", args: []string{"-e", appleScript(n)}},
		}
	case "linux":
		return []candidate{{bin: "notify-send", args: notifySendArgs(n)}}
	default:
		return nil
	}
}

func terminalNotifierArgs(n Notification) []string {
	args := []string{"-title", n.Title, "-message", n.Body}
	if n.Subtitle != "" {
		args = append(args, "-subtitle", n.Subtitle)
	}
	if n.Group != "" {
		args = append(args, "-group", n.Group)
	}
	// Hand the click back to the terminal the gate ran in, so "Show" returns the
	// developer to their work rather than to an unrelated app.
	if sender := senderBundle(os.Getenv("TERM_PROGRAM")); sender != "" {
		args = append(args, "-sender", sender)
	}
	return args
}

// senderBundle maps TERM_PROGRAM to the bundle id of the terminal to activate
// when the notification is clicked. An unrecognized (or absent) terminal yields
// "", which leaves terminal-notifier's own identity in place — still correct,
// just without the click-through.
func senderBundle(termProgram string) string {
	switch termProgram {
	case "Apple_Terminal":
		return "com.apple.Terminal"
	case "iTerm.app":
		return "com.googlecode.iterm2"
	case "ghostty":
		return "com.mitchellh.ghostty"
	case "WezTerm":
		return "com.github.wez.wezterm"
	case "Alacritty":
		return "org.alacritty"
	case "vscode":
		return "com.microsoft.VSCode"
	case "Hyper":
		return "co.zeit.hyper"
	case "kitty":
		return "net.kovidgoyal.kitty"
	case "Warp":
		return "dev.warp.Warp-Stable"
	default:
		return ""
	}
}

// appleScript renders the osascript fallback. AppleScript has a subtitle slot,
// so the scope line survives even on the degraded path.
func appleScript(n Notification) string {
	s := "display notification " + quote(n.Body) + " with title " + quote(n.Title)
	if n.Subtitle != "" {
		s += " subtitle " + quote(n.Subtitle)
	}
	return s
}

// notifySendArgs renders the Linux form. -a names warden as the source (rather
// than leaving the notification unattributed), and a blocked push is raised to
// critical so it persists instead of auto-dismissing while the developer is
// away — which is precisely the case the notification exists for.
func notifySendArgs(n Notification) []string {
	body := n.Body
	if n.Subtitle != "" {
		// notify-send has no subtitle slot; fold it in rather than drop it.
		body = n.Subtitle + " — " + body
	}
	args := []string{"-a", "warden"}
	if n.Urgent {
		args = append(args, "-u", "critical")
	}
	return append(args, n.Title, body)
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
