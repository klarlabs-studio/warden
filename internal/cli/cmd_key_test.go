package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestKeyList(t *testing.T) {
	t.Run("lists the committed roster", func(t *testing.T) {
		repoWithConfig(t, "trusted_keys:\n  - 0123456789abcdef\n  - fedcba9876543210\n")
		var out, errb bytes.Buffer
		if code := cmdKey([]string{"list"}, &out, &errb); code != 0 {
			t.Fatalf("key list: code=%d err=%q", code, errb.String())
		}
		got := out.String()
		if !strings.Contains(got, "trusted signers (2)") || !strings.Contains(got, "0123456789abcdef") {
			t.Errorf("expected the roster listed, got:\n%s", got)
		}
	})

	t.Run("explains when there is no roster", func(t *testing.T) {
		repoWithConfig(t, "")
		var out, errb bytes.Buffer
		if code := cmdKey([]string{"list"}, &out, &errb); code != 0 {
			t.Fatalf("key list (empty): code=%d err=%q", code, errb.String())
		}
		if !strings.Contains(out.String(), "no trusted_keys") {
			t.Errorf("expected an empty-roster explanation, got:\n%s", out.String())
		}
	})

	t.Run("show still prints this machine's fingerprint", func(t *testing.T) {
		repoWithConfig(t, "")
		var out, errb bytes.Buffer
		if code := cmdKey([]string{"show"}, &out, &errb); code != 0 {
			t.Fatalf("key show: code=%d err=%q", code, errb.String())
		}
		if !strings.Contains(out.String(), "fingerprint:") {
			t.Errorf("expected a fingerprint line, got:\n%s", out.String())
		}
	})

	t.Run("unknown subcommand is a usage error", func(t *testing.T) {
		repoWithConfig(t, "")
		var out, errb bytes.Buffer
		if code := cmdKey([]string{"frobnicate"}, &out, &errb); code != 2 {
			t.Errorf("expected exit 2 for unknown subcommand, got %d", code)
		}
	})
}
