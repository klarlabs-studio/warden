package domain

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// Every built-in entry cites the documentation it comes from, so a reader can
// check it and a later change has something to be checked against.
func TestAuthorshipTables_EveryEntryCitesItsSource(t *testing.T) {
	for _, r := range AgentTrailerRules() {
		if r.Agent == "" || r.Key == "" || !strings.HasPrefix(r.Source, "https://") {
			t.Errorf("trailer rule %+v needs an agent, a key and a documentation source", r)
		}
	}
	for _, r := range AgentEnvMarkerRules() {
		if r.Agent == "" || r.Name == "" || r.Value == "" || !strings.HasPrefix(r.Source, "https://") {
			t.Errorf("env marker rule %+v needs an agent, a name, a value and a documentation source", r)
		}
	}
}

func TestObserveAuthorship_Trailers(t *testing.T) {
	cases := []struct {
		name    string
		trailer Trailer
		agent   string // "" = must not match
	}{
		{"claude co-author", Trailer{"Co-Authored-By", "Claude Opus 5 <noreply@anthropic.com>"}, "claude-code"},
		{"github spelling", Trailer{"Co-authored-by", "Claude <noreply@anthropic.com>"}, "claude-code"},
		{"lower case, odd spacing", Trailer{" co-authored-by ", "  Claude Code   <NoReply@Anthropic.com>  "}, "claude-code"},
		{"session link", Trailer{"Claude-Session", "https://claude.ai/code/session_X"}, "claude-code"},
		{"session key any case", Trailer{"claude-session", "x"}, "claude-code"},
		{"a person co-author", Trailer{"Co-Authored-By", "Alice <alice@example.invalid>"}, ""},
		{"address only in the name", Trailer{"Co-Authored-By", "noreply@anthropic.com fan <fan@example.invalid>"}, ""},
		{"address under another key", Trailer{"Reviewed-By", "Claude <noreply@anthropic.com>"}, ""},
		{"signed-off-by", Trailer{"Signed-off-by", "warden-test"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ObserveAuthorship([]CommitTrailers{{Commit: "c1", Trailers: []Trailer{tc.trailer}}}, nil)
			if tc.agent == "" {
				if got != nil {
					t.Fatalf("%+v must not read as an agent declaration: %+v", tc.trailer, got)
				}
				return
			}
			if !got.Declared() || len(got.Trailers) != 1 || got.Trailers[0].Agent != tc.agent || got.Trailers[0].Commit != "c1" {
				t.Fatalf("%+v must declare %s on c1, got %+v", tc.trailer, tc.agent, got)
			}
			if want := tc.trailer.Key + ": " + tc.trailer.Value; got.Trailers[0].Trailer != want {
				t.Errorf("recorded trailer %q, want it as git parsed it, %q", got.Trailers[0].Trailer, want)
			}
		})
	}
}

func TestObserveAuthorship_EnvMarkers(t *testing.T) {
	cases := []struct {
		name    string
		environ []string
		want    []EnvMarkerDeclaration
	}{
		{"claude code child process", []string{"CLAUDE_CODE_CHILD_SESSION=1"}, []EnvMarkerDeclaration{{Name: "CLAUDE_CODE_CHILD_SESSION", Agent: "claude-code"}}},
		{"claude code cloud", []string{"CLAUDE_CODE_REMOTE=true"}, []EnvMarkerDeclaration{{Name: "CLAUDE_CODE_REMOTE", Agent: "claude-code"}}},
		{"gemini cli", []string{"GEMINI_CLI=1"}, []EnvMarkerDeclaration{{Name: "GEMINI_CLI", Agent: "gemini-cli"}}},
		// CLAUDECODE is also set by IDE extensions in a person's terminal.
		{"CLAUDECODE alone is not a marker", []string{"CLAUDECODE=1"}, nil},
		{"undocumented value", []string{"GEMINI_CLI=0", "CLAUDE_CODE_REMOTE=false", "CLAUDE_CODE_CHILD_SESSION="}, nil},
		{"name as a substring", []string{"MY_GEMINI_CLI=1", "GEMINI_CLI_X=1"}, nil},
		{"several, table order", []string{"PATH=/bin", "GEMINI_CLI=1", "CLAUDE_CODE_CHILD_SESSION=1"}, []EnvMarkerDeclaration{
			{Name: "CLAUDE_CODE_CHILD_SESSION", Agent: "claude-code"}, {Name: "GEMINI_CLI", Agent: "gemini-cli"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ObserveAuthorship(nil, tc.environ)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("got %+v, want no declaration", got)
				}
				return
			}
			if !reflect.DeepEqual(got.EnvMarkers, tc.want) {
				t.Fatalf("got %+v, want %+v", got.EnvMarkers, tc.want)
			}
		})
	}
}

// Nothing observed is nil, not an empty object: a profiled note then says
// nothing about authorship rather than something that reads as "human".
func TestObserveAuthorship_NothingObservedIsNil(t *testing.T) {
	got := ObserveAuthorship([]CommitTrailers{{Commit: "c1"}, {Commit: "c2", Trailers: []Trailer{{"Signed-off-by", "x"}}}}, []string{"HOME=/h"})
	if got != nil || got.Declared() || got.Summary() != "" {
		t.Fatalf("got %+v, want nil", got)
	}
}

// Only table names reach the record: neither values nor other variables.
func TestObserveAuthorship_RecordsNoEnvironmentValues(t *testing.T) {
	got := ObserveAuthorship(nil, []string{"GEMINI_CLI=1", "ANTHROPIC_API_KEY=sk-secret"})
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"env_markers":[{"name":"GEMINI_CLI","agent":"gemini-cli"}]}`; string(b) != want {
		t.Fatalf("got %s, want %s", b, want)
	}
}

func TestAuthorshipSignals_Summary(t *testing.T) {
	s := ObserveAuthorship([]CommitTrailers{
		{Commit: "0123456789abcdef", Trailers: []Trailer{{"Claude-Session", "u1"}, {"Claude-Session", "u2"}}},
		{Commit: "fedcba9876543210", Trailers: []Trailer{{"Claude-Session", "u3"}}},
	}, []string{"GEMINI_CLI=1"})
	got := s.Summary()
	for _, want := range []string{`commit 0123456789ab carries "Claude-Session: u1"`, "and 1 more"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary %q must contain %q", got, want)
		}
	}
}
