package forge

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// A gh that answers the protection endpoint with a scripted status and body.
const fakeProtectionGH = `#!/bin/sh
if [ -n "$GH_PROT_STDERR" ]; then echo "$GH_PROT_STDERR" >&2; fi
if [ "${GH_PROT_SILENT:-}" = "1" ]; then exit "${GH_PROT_EXIT:-1}"; fi
# No ${VAR:-default} around the body: a JSON default like {} makes the shell
# read "${B:-{}" as the expansion and leave a stray "}" behind, which appends a
# brace and makes every reply unparseable. The helper always sets the variable.
printf 'HTTP/2.0 %s Status\r\nContent-Type: application/json\r\n\r\n%s\n' \
  "${GH_PROT_STATUS:-200}" "$GH_PROT_BODY"
exit "${GH_PROT_EXIT:-0}"
`

func withProtectionGH(t *testing.T, status, body string) *GH {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake gh is a shell script; warden is unix-first")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(fakeProtectionGH), 0o755); err != nil { //nolint:gosec // test fixture must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GH_PROT_STATUS", status)
	t.Setenv("GH_PROT_BODY", body)
	t.Setenv("GH_PROT_EXIT", "0")
	t.Setenv("GH_PROT_SILENT", "0")
	t.Setenv("GH_PROT_STDERR", "")
	return NewGH(t.TempDir())
}

// THE security case. Reading branch protection needs admin rights on GitHub, so
// an ordinary token gets 403 — and a 403 folded into "this branch has no
// protection" would put a control-gap accusation in an evidence document on the
// strength of a permission warden does not hold.
//
// 404 is the opposite: the forge answering that there IS no rule. That is a
// finding and must survive.
func TestProtectionFor_DistinguishesCannotReadFromNoRule(t *testing.T) {
	t.Run("403 is unknown, not unprotected", func(t *testing.T) {
		g := withProtectionGH(t, "403", `{"message":"Must have admin rights"}`)
		t.Setenv("GH_PROT_EXIT", "1")
		t.Setenv("GH_PROT_STDERR", "gh: Must have admin rights to Repository. (HTTP 403)")

		p := g.ProtectionFor(context.Background(), "main")
		if p.Known {
			t.Fatal("a 403 must not be reported as a known rule")
		}
		if p.Protected {
			t.Error("an unreadable rule must never claim the branch is protected either way")
		}
		if p.Reason == "" {
			t.Error("the operator must be told why it could not be read")
		}
	})

	t.Run("404 is a real answer: no rule", func(t *testing.T) {
		g := withProtectionGH(t, "404", `{"message":"Branch not protected"}`)
		t.Setenv("GH_PROT_EXIT", "1") // gh exits non-zero on 404 too
		p := g.ProtectionFor(context.Background(), "main")
		if !p.Known {
			t.Fatal("a 404 IS an answer and must be Known")
		}
		if p.Protected {
			t.Error("404 means the branch carries no protection rule")
		}
	})

	t.Run("no reply at all is unknown", func(t *testing.T) {
		g := withProtectionGH(t, "200", "")
		t.Setenv("GH_PROT_SILENT", "1")
		t.Setenv("GH_PROT_EXIT", "1")
		if p := g.ProtectionFor(context.Background(), "main"); p.Known {
			t.Error("a gh that printed no HTTP response must be unknown")
		}
	})
}

// The fields an auditor's conclusion turns on must survive the round trip, and
// enforce_admins in particular: without it, "2 reviews required" reads as
// unskippable when it is a default any admin may merge past.
func TestProtectionFor_ReadsTheFieldsThatChangeTheConclusion(t *testing.T) {
	body := `{"required_pull_request_reviews":{"required_approving_review_count":2,` +
		`"dismiss_stale_reviews":true,"require_last_push_approval":true},` +
		`"required_conversation_resolution":{"enabled":true},"enforce_admins":{"enabled":true}}`
	g := withProtectionGH(t, "200", body)

	p := g.ProtectionFor(context.Background(), "main")
	if !p.Known || !p.Protected {
		t.Fatalf("a 200 with a rule must be known and protected: %+v", p)
	}
	if p.RequiredApprovals != 2 {
		t.Errorf("RequiredApprovals = %d, want 2", p.RequiredApprovals)
	}
	for name, got := range map[string]bool{
		"DismissStaleReviews":           p.DismissStaleReviews,
		"RequireLastPushApproval":       p.RequireLastPushApproval,
		"RequireConversationResolution": p.RequireConversationResolution,
		"EnforceAdmins":                 p.EnforceAdmins,
	} {
		if !got {
			t.Errorf("%s = false, want true", name)
		}
	}
	if !p.RequiresIndependentReview() || !p.Enforceable() {
		t.Error("2 required approvals with enforce_admins is a requirement that binds everyone")
	}
}

// GitHub omits a block entirely when the setting is off, so a nil check is the
// difference between "absent" and "present but false". Both mean off here, and
// neither may be guessed at as on.
func TestProtectionFor_AbsentBlocksAreOffNotAssumed(t *testing.T) {
	g := withProtectionGH(t, "200", `{"url":"https://api.github.com/x"}`)
	p := g.ProtectionFor(context.Background(), "main")

	if !p.Known || !p.Protected {
		t.Fatalf("a 200 means a rule exists: %+v", p)
	}
	if p.RequiredApprovals != 0 || p.EnforceAdmins || p.RequireConversationResolution {
		t.Errorf("absent blocks must read as off, got %+v", p)
	}
	// Protected with zero required approvals is a real configuration, not a
	// contradiction — warden's own main branch is exactly this.
	if p.RequiresIndependentReview() {
		t.Error("no required_pull_request_reviews block must not imply review is required")
	}
}

// A 200 whose body will not parse is not an answer.
func TestProtectionFor_UnparseableBodyIsUnknown(t *testing.T) {
	g := withProtectionGH(t, "200", `{not json`)
	if p := g.ProtectionFor(context.Background(), "main"); p.Known {
		t.Error("an unparseable 200 must be unknown, not a rule")
	}
}
