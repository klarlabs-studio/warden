package notify

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// installed stubs lookPath so a test decides which notifiers "exist",
// independent of what the dev machine happens to have.
func installed(t *testing.T, present ...string) {
	t.Helper()
	orig := lookPath
	t.Cleanup(func() { lookPath = orig })
	set := make(map[string]bool, len(present))
	for _, p := range present {
		set[p] = true
	}
	lookPath = func(name string) (string, error) {
		if set[name] {
			return "/usr/bin/" + name, nil
		}
		return "", errors.New("not found")
	}
}

// The whole point of preferring terminal-notifier: an osascript notification is
// owned by Script Editor, so it is blocked unless the user granted Script
// Editor access and clicking it opens an empty script window.
func TestResolve_DarwinPrefersTerminalNotifier(t *testing.T) {
	installed(t, "terminal-notifier", "osascript")
	bin, args := resolve("darwin", Notification{Title: "warden: pre-push failed", Body: "step lint failed"})
	if bin != "terminal-notifier" {
		t.Fatalf("darwin notifier = %q, want terminal-notifier", bin)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"-title", "warden: pre-push failed", "-message", "step lint failed"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args %q missing %q", joined, want)
		}
	}
}

func TestResolve_DarwinFallsBackToOsascript(t *testing.T) {
	installed(t, "osascript")
	bin, args := resolve("darwin", Notification{Title: "warden: pre-push passed", Body: `pushed "main"`})
	if bin != "osascript" {
		t.Fatalf("fallback = %q, want osascript", bin)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "display notification") || !strings.Contains(joined, "with title") {
		t.Errorf("osascript args missing pieces: %q", joined)
	}
	// The embedded quote in the body must be escaped, not left to break the
	// AppleScript string.
	if !strings.Contains(joined, `\"main\"`) {
		t.Errorf("body quote not escaped: %q", joined)
	}
}

func TestResolve_NoNotifierInstalledIsANoOp(t *testing.T) {
	installed(t) // nothing present
	if bin, _ := resolve("darwin", Notification{Title: "t"}); bin != "" {
		t.Errorf("no notifier installed should yield no command, got %q", bin)
	}
	if bin, _ := resolve("plan9", Notification{Title: "t"}); bin != "" {
		t.Errorf("unsupported platform should yield no command, got %q", bin)
	}
}

func TestResolve_LinuxUsesNotifySend(t *testing.T) {
	installed(t, "notify-send")
	bin, args := resolve("linux", Notification{
		Title: "warden: pre-push failed", Subtitle: "warden · main", Body: "step lint failed", Urgent: true,
	})
	if bin != "notify-send" {
		t.Fatalf("linux notifier = %q, want notify-send", bin)
	}
	joined := strings.Join(args, " ")
	// Named as warden rather than left unattributed, raised to critical so a
	// blocked push persists, and the subtitle folded in (no subtitle slot).
	for _, want := range []string{"-a warden", "-u critical", "warden · main — step lint failed"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args %q missing %q", joined, want)
		}
	}
	// A passing verdict is not urgent.
	_, args = resolve("linux", Notification{Title: "warden: pre-push passed", Body: "pushed"})
	if strings.Contains(strings.Join(args, " "), "-u critical") {
		t.Error("a passing run must not be raised to critical")
	}
}

func TestTerminalNotifierArgs_SubtitleAndGroup(t *testing.T) {
	args := strings.Join(terminalNotifierArgs(Notification{
		Title: "t", Subtitle: "warden · main", Body: "b", Group: "warden-warden",
	}), " ")
	for _, want := range []string{"-subtitle warden · main", "-group warden-warden"} {
		if !strings.Contains(args, want) {
			t.Errorf("args %q missing %q", args, want)
		}
	}
	// Optional fields are omitted rather than passed empty.
	bare := strings.Join(terminalNotifierArgs(Notification{Title: "t", Body: "b"}), " ")
	if strings.Contains(bare, "-subtitle") || strings.Contains(bare, "-group") {
		t.Errorf("empty optional fields should be omitted: %q", bare)
	}
}

func TestSenderBundle(t *testing.T) {
	tests := map[string]string{
		"Apple_Terminal": "com.apple.Terminal",
		"iTerm.app":      "com.googlecode.iterm2",
		"ghostty":        "com.mitchellh.ghostty",
		"vscode":         "com.microsoft.VSCode",
		// An unknown or absent terminal leaves terminal-notifier's own identity
		// in place rather than guessing a bundle that may not exist.
		"SomeNewTerminal": "",
		"":                "",
	}
	for in, want := range tests {
		if got := senderBundle(in); got != want {
			t.Errorf("senderBundle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAppleScript_IncludesSubtitleWhenSet(t *testing.T) {
	if s := appleScript(Notification{Title: "t", Body: "b", Subtitle: "warden · main"}); !strings.Contains(s, "subtitle") {
		t.Errorf("subtitle dropped on the fallback path: %q", s)
	}
	if s := appleScript(Notification{Title: "t", Body: "b"}); strings.Contains(s, "subtitle") {
		t.Errorf("empty subtitle should be omitted: %q", s)
	}
}

// The macOS fallback fails silently and invisibly, so the degraded state has to
// be nameable — and the healthy state has to stay quiet.
func TestAdvice(t *testing.T) {
	t.Run("darwin without terminal-notifier explains the Script Editor trap", func(t *testing.T) {
		installed(t, "osascript")
		got := advice("darwin")
		for _, want := range []string{"Script Editor", "terminal-notifier"} {
			if !strings.Contains(got, want) {
				t.Errorf("advice %q missing %q", got, want)
			}
		}
	})

	t.Run("darwin with terminal-notifier is silent", func(t *testing.T) {
		installed(t, "terminal-notifier", "osascript")
		if got := advice("darwin"); got != "" {
			t.Errorf("a working setup needs no advice, got %q", got)
		}
	})

	t.Run("no notifier at all is silent", func(t *testing.T) {
		// Nothing to fix and nothing warden can post through: advice would be noise.
		installed(t)
		if got := advice("darwin"); got != "" {
			t.Errorf("got %q, want silence", got)
		}
	})

	t.Run("linux advises libnotify only when notify-send is missing", func(t *testing.T) {
		installed(t)
		if got := advice("linux"); !strings.Contains(got, "notify-send") {
			t.Errorf("advice %q should name notify-send", got)
		}
		installed(t, "notify-send")
		if got := advice("linux"); got != "" {
			t.Errorf("a working setup needs no advice, got %q", got)
		}
	})

	t.Run("unsupported platform is silent", func(t *testing.T) {
		installed(t)
		if got := advice("plan9"); got != "" {
			t.Errorf("got %q, want silence", got)
		}
	})
}

func TestQuote(t *testing.T) {
	if got := quote(`a"b\c`); got != `"a\"b\\c"` {
		t.Errorf("quote = %q", got)
	}
}

func TestSend_IsBestEffortAndNeverPanics(t *testing.T) {
	// Stub the shell-out so the test never pops a real desktop notification on a
	// machine that has a notifier (a dev's macOS box).
	orig := runNotifier
	t.Cleanup(func() { runNotifier = orig })
	var gotName string
	runNotifier = func(_ context.Context, name string, _ ...string) error {
		gotName = name
		return nil
	}

	// Nothing installed: Send must stop before the seam.
	installed(t)
	Send(Notification{Title: "warden: pre-push passed", Body: `pushed "main"`})
	if gotName != "" {
		t.Errorf("Send invoked %q with no notifier installed", gotName)
	}

	// A notifier present: Send routes the platform command through the seam and
	// never invokes anything else.
	lookPath = func(string) (string, error) { return "/usr/bin/stub", nil }
	Send(Notification{Title: "warden: pre-push failed", Body: "step lint failed"})
	if want, _ := resolve("darwin", Notification{Title: "x"}); gotName != "" && gotName != want && gotName != "notify-send" {
		t.Errorf("Send invoked %q, want a platform notifier", gotName)
	}
}

// lookPath must default to the real exec.LookPath so production behavior is
// not accidentally left stubbed.
func TestLookPathDefaultsToExec(t *testing.T) {
	if _, err := exec.LookPath("sh"); err == nil {
		if _, err := lookPath("sh"); err != nil {
			t.Errorf("lookPath should resolve sh like exec.LookPath: %v", err)
		}
	}
}
