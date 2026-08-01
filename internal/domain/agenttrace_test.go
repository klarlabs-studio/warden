package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const validTrace = `{
  "version": "0.1.0",
  "id": "9f2e8a1b-0000-4000-8000-000000000001",
  "timestamp": "2026-08-01T10:00:00Z",
  "files": [{"path":"main.go","conversations":[{"ranges":[{"start":1,"end":20,"contributor":"ai"}]}]}]
}`

func TestNewAgentTraceRef_ExtractsIdentityAndDigest(t *testing.T) {
	ref, err := NewAgentTraceRef(".agent-trace.json", []byte(validTrace))
	if err != nil {
		t.Fatalf("NewAgentTraceRef: %v", err)
	}
	if ref.SpecVersion != "0.1.0" {
		t.Errorf("spec version = %q", ref.SpecVersion)
	}
	if ref.TraceID == "" || ref.Path != ".agent-trace.json" {
		t.Errorf("identity not captured: %+v", ref)
	}
	if len(ref.Digest) != 64 {
		t.Errorf("digest = %q, want a hex sha256", ref.Digest)
	}
}

// The whole value of notarizing is that a later edit is detectable. This is the
// case that matters: an agent's claim about WHO wrote the code, rewritten after
// the fact. Every other Agent Trace implementation is a self-report with nothing
// stopping exactly this.
func TestAgentTraceRef_DetectsARewrittenClaim(t *testing.T) {
	ref, err := NewAgentTraceRef(".agent-trace.json", []byte(validTrace))
	if err != nil {
		t.Fatal(err)
	}
	if !ref.Matches([]byte(validTrace)) {
		t.Fatal("the original record must match")
	}

	var doc map[string]any
	if err := json.Unmarshal([]byte(validTrace), &doc); err != nil {
		t.Fatal(err)
	}
	files := doc["files"].([]any)
	convs := files[0].(map[string]any)["conversations"].([]any)
	ranges := convs[0].(map[string]any)["ranges"].([]any)
	ranges[0].(map[string]any)["contributor"] = "human" // the lie
	edited, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if ref.Matches(edited) {
		t.Error("rewriting an AI contribution as human must be detected")
	}
}

// Byte-exact on purpose: warden cannot tell a harmless reformat from a
// substantive edit, so it reports the difference rather than canonicalising and
// quietly accepting a record that is no longer the one that was signed.
func TestAgentTraceRef_ReformattingCountsAsAChange(t *testing.T) {
	ref, err := NewAgentTraceRef("t.json", []byte(validTrace))
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err := json.Unmarshal([]byte(validTrace), &doc); err != nil {
		t.Fatal(err)
	}
	reformatted, err := json.Marshal(doc) // same data, different bytes
	if err != nil {
		t.Fatal(err)
	}
	if ref.Matches(reformatted) {
		t.Error("a reformatted record must not silently match")
	}
}

// Notarizing the wrong file would put a meaningless digest in a signed record —
// worse than notarizing nothing, because the note would look complete.
func TestNewAgentTraceRef_RejectsWhatIsNotATrace(t *testing.T) {
	cases := map[string]string{
		"not json":         `not json at all`,
		"missing version":  `{"id":"x","files":[]}`,
		"missing id":       `{"version":"0.1.0","files":[]}`,
		"missing files":    `{"version":"0.1.0","id":"x"}`,
		"unrelated config": `{"name":"my-app","dependencies":{}}`,
	}
	for name, raw := range cases {
		if _, err := NewAgentTraceRef("f.json", []byte(raw)); !errors.Is(err, ErrNotAgentTrace) {
			t.Errorf("%s: err = %v, want ErrNotAgentTrace", name, err)
		}
	}
}

// The spec is a draft RFC and will move. Warden reads only what identifies the
// record, so an unfamiliar future revision is still notarisable — the digest is
// what carries the guarantee, and that holds whatever the schema becomes.
func TestNewAgentTraceRef_ToleratesUnknownFields(t *testing.T) {
	future := `{
	  "version": "9.9.9",
	  "id": "future-id",
	  "files": [],
	  "somethingEntirelyNew": {"nested": [1,2,3]},
	  "metadata": {"dev.cursor": {"workspace_id": "ws-1"}}
	}`
	ref, err := NewAgentTraceRef("t.json", []byte(future))
	if err != nil {
		t.Fatalf("a future revision must still notarize: %v", err)
	}
	if ref.SpecVersion != "9.9.9" {
		t.Errorf("the declared version must be recorded verbatim, got %q", ref.SpecVersion)
	}
}

// An empty reference must never report a match, or a record with no digest
// would appear verified.
func TestAgentTraceRef_EmptyNeverMatches(t *testing.T) {
	var ref AgentTraceRef
	if ref.Matches([]byte(validTrace)) || ref.Matches(nil) {
		t.Error("a reference with no digest must never match")
	}
}

// Like every other addition to the record, this must be invisible when unused,
// or notes written before the field existed stop verifying.
func TestRunRecord_AgentTraceIsOmittedWhenAbsent(t *testing.T) {
	rec := RunRecord{RunID: "run-1", CommitSHA: "abc"}
	payload, err := rec.SigningPayload()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "agent_trace") {
		t.Errorf("an unused agent_trace must not appear in the payload:\n%s", payload)
	}
}

// And when present it must be COVERED by the signature: a digest bound alongside
// the record rather than inside it would be as editable as the trace it vouches
// for.
func TestRunRecord_AgentTraceIsInsideTheSignedPayload(t *testing.T) {
	ref, err := NewAgentTraceRef("t.json", []byte(validTrace))
	if err != nil {
		t.Fatal(err)
	}
	rec := RunRecord{RunID: "run-1", CommitSHA: "abc", AgentTrace: &ref}
	payload, err := rec.SigningPayload()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), ref.Digest) {
		t.Error("the trace digest must be inside the bytes the signature covers")
	}
}
