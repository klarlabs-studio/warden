package stepsdk_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"go.klarlabs.de/warden/stepsdk"
)

// sampleInput is a representative wire payload matching the spec's example.
const sampleInput = `{
  "schema_version": 1,
  "step_id": "security-scan",
  "hook": "pre_push",
  "repo_path": "/tmp/warden-worktree-abc123",
  "branch": "feature/login-fix",
  "diff_summary": { "files_touched": 6, "lines_changed": 210 },
  "resolved_agent": "codex",
  "prior_findings": []
}`

func TestRunWith(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		input      string
		handler    stepsdk.Handler
		wantStatus stepsdk.Status
		wantFixed  bool
		wantFinds  int
		wantErr    bool
	}{
		{
			name:  "pass round-trip",
			input: sampleInput,
			handler: func(in stepsdk.Input) stepsdk.Output {
				// Handler sees the decoded input verbatim.
				if in.StepID != "security-scan" || in.Hook != "pre_push" {
					t.Errorf("unexpected input: %+v", in)
				}
				if in.DiffSummary.FilesTouched != 6 || in.DiffSummary.LinesChanged != 210 {
					t.Errorf("unexpected diff summary: %+v", in.DiffSummary)
				}
				return stepsdk.Pass()
			},
			wantStatus: stepsdk.StatusPass,
		},
		{
			name:  "fail round-trip with finding",
			input: sampleInput,
			handler: func(stepsdk.Input) stepsdk.Output {
				return stepsdk.Fail(stepsdk.Finding{
					Severity: "high",
					Message:  "hardcoded secret",
					File:     "auth/token.go",
					Line:     42,
				})
			},
			wantStatus: stepsdk.StatusFail,
			wantFinds:  1,
		},
		{
			name:  "needs approval round-trip",
			input: sampleInput,
			handler: func(stepsdk.Input) stepsdk.Output {
				return stepsdk.NeedsApproval(stepsdk.Finding{Severity: "medium", Message: "review needed"})
			},
			wantStatus: stepsdk.StatusNeedsApproval,
			wantFinds:  1,
		},
		{
			name:  "handler reports a fix",
			input: sampleInput,
			handler: func(stepsdk.Input) stepsdk.Output {
				out := stepsdk.Pass()
				out.Fixed = true
				return out
			},
			wantStatus: stepsdk.StatusPass,
			wantFixed:  true,
		},
		{
			name:  "malformed input fails closed",
			input: `{not valid json`,
			handler: func(stepsdk.Input) stepsdk.Output {
				t.Error("handler must not run on malformed input")
				return stepsdk.Pass()
			},
			wantStatus: stepsdk.StatusFail,
			wantFinds:  1,
			wantErr:    true,
		},
		{
			name:  "empty input fails closed",
			input: ``,
			handler: func(stepsdk.Input) stepsdk.Output {
				t.Error("handler must not run on empty input")
				return stepsdk.Pass()
			},
			wantStatus: stepsdk.StatusFail,
			wantFinds:  1,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer
			err := stepsdk.RunWith(strings.NewReader(tt.input), &out, tt.handler)

			if tt.wantErr && err == nil {
				t.Fatal("expected an error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			var got stepsdk.Output
			if decErr := json.Unmarshal(out.Bytes(), &got); decErr != nil {
				t.Fatalf("output is not valid JSON: %v (raw: %q)", decErr, out.String())
			}

			if got.SchemaVersion != stepsdk.SchemaVersion {
				t.Errorf("schema_version = %d, want %d", got.SchemaVersion, stepsdk.SchemaVersion)
			}
			if got.Status != tt.wantStatus {
				t.Errorf("status = %q, want %q", got.Status, tt.wantStatus)
			}
			if got.Fixed != tt.wantFixed {
				t.Errorf("fixed = %v, want %v", got.Fixed, tt.wantFixed)
			}
			if len(got.Findings) != tt.wantFinds {
				t.Errorf("findings count = %d, want %d", len(got.Findings), tt.wantFinds)
			}
		})
	}
}

// TestRunWithStampsSchemaVersion verifies RunWith is authoritative about the
// schema version even when a handler returns a bare/wrong value.
func TestRunWithStampsSchemaVersion(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	err := stepsdk.RunWith(strings.NewReader(sampleInput), &out, func(stepsdk.Input) stepsdk.Output {
		return stepsdk.Output{Status: stepsdk.StatusPass, SchemaVersion: 99}
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var got stepsdk.Output
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if got.SchemaVersion != stepsdk.SchemaVersion {
		t.Errorf("schema_version = %d, want %d (must be stamped by SDK)", got.SchemaVersion, stepsdk.SchemaVersion)
	}
}

// errWriter always fails, to exercise the write-error path.
type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, errors.New("boom") }

// TestRunWithWriteErrorOnMalformedInput ensures the original decode error is
// surfaced even when writing the fail report also fails.
func TestRunWithWriteErrorOnMalformedInput(t *testing.T) {
	t.Parallel()

	err := stepsdk.RunWith(strings.NewReader(`{bad`), errWriter{}, func(stepsdk.Input) stepsdk.Output {
		return stepsdk.Pass()
	})
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "decode input") {
		t.Errorf("error should mention the decode failure, got: %v", err)
	}
}

func TestHelpers(t *testing.T) {
	t.Parallel()

	find := stepsdk.Finding{Severity: "low", Message: "note"}

	tests := []struct {
		name       string
		got        stepsdk.Output
		wantStatus stepsdk.Status
		wantFinds  int
	}{
		{"pass no findings", stepsdk.Pass(), stepsdk.StatusPass, 0},
		{"pass with findings", stepsdk.Pass(find), stepsdk.StatusPass, 1},
		{"fail with findings", stepsdk.Fail(find, find), stepsdk.StatusFail, 2},
		{"needs approval", stepsdk.NeedsApproval(find), stepsdk.StatusNeedsApproval, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.got.SchemaVersion != stepsdk.SchemaVersion {
				t.Errorf("schema_version = %d, want %d", tt.got.SchemaVersion, stepsdk.SchemaVersion)
			}
			if tt.got.Status != tt.wantStatus {
				t.Errorf("status = %q, want %q", tt.got.Status, tt.wantStatus)
			}
			if len(tt.got.Findings) != tt.wantFinds {
				t.Errorf("findings count = %d, want %d", len(tt.got.Findings), tt.wantFinds)
			}
		})
	}
}

// TestPassOmitsEmptyFindings verifies that a pass with no findings serializes
// without a findings key, keeping the wire payload minimal.
func TestPassOmitsEmptyFindings(t *testing.T) {
	t.Parallel()

	b, err := json.Marshal(stepsdk.Pass())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "findings") {
		t.Errorf("expected no findings key, got: %s", b)
	}
}
