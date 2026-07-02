package notify

import (
	"strings"
	"testing"
)

func TestCommand(t *testing.T) {
	t.Run("darwin scripts osascript", func(t *testing.T) {
		name, args := command("darwin", "warden: passed", `pushed "main"`)
		if name != "osascript" {
			t.Fatalf("darwin notifier = %q, want osascript", name)
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
	})

	t.Run("linux uses notify-send", func(t *testing.T) {
		name, args := command("linux", "warden: failed", "gate blocked")
		if name != "notify-send" || len(args) != 2 || args[0] != "warden: failed" {
			t.Errorf("linux notifier = %q %v", name, args)
		}
	})

	t.Run("unsupported platform is a no-op", func(t *testing.T) {
		if name, _ := command("plan9", "t", "b"); name != "" {
			t.Errorf("unsupported platform should return no command, got %q", name)
		}
	})
}

func TestQuote(t *testing.T) {
	if got := quote(`a"b\c`); got != `"a\"b\\c"` {
		t.Errorf("quote = %q", got)
	}
}

func TestSend_UnsupportedPlatformDoesNotPanic(t *testing.T) {
	// Send must never panic or error regardless of environment.
	Send("title", "body")
}
