package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// feedback_status reads an advisory document. It must be marked read-only so a
// client can call it without the trust opt-in run_trigger needs, and it must
// hand the reader's judgement through unchanged.
func TestFeedbackStatusTool(t *testing.T) {
	doc := &domain.FeedbackDocument{Schema: domain.FeedbackSchema, State: domain.FeedbackComplete, Freshness: domain.FreshnessStaleSnapshot}
	f := &fakeFacade{feedback: FeedbackStatusOutput{Present: true, Diagnostics: false, FreshnessNote: "changed", Document: doc}}
	// A server whose gate refuses every run must still answer.
	srv := NewServer(f, "1.2.3", func() error { return errors.New("untrusted") })
	readOnly := false
	for _, info := range srv.Tools() {
		if info.Name == "feedback_status" {
			readOnly = info.Annotations != nil && info.Annotations.ReadOnlyHint != nil && *info.Annotations.ReadOnlyHint
		}
	}
	if !readOnly {
		t.Fatal("feedback_status is not annotated read-only")
	}
	tool, ok := srv.GetTool("feedback_status")
	if !ok {
		t.Fatal("feedback_status not registered")
	}
	got, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var out FeedbackStatusOutput
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("%v: %s", err, data)
	}
	if !out.Present || out.Diagnostics || out.Document == nil || out.Document.Freshness != domain.FreshnessStaleSnapshot {
		t.Fatalf("output: %s", data)
	}

	f.feedbackErr = errors.New("unreadable")
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("a reader error was swallowed")
	}
}
