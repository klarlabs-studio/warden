package forge

import (
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

type row = struct {
	State string `json:"state"`
}

func TestTally(t *testing.T) {
	cases := []struct {
		name  string
		rows  []row
		want  domain.CIState
		pass  int
		fail  int
		pend  int
		total int
	}{
		{"none", nil, domain.CINone, 0, 0, 0, 0},
		{"all pass", []row{{"SUCCESS"}, {"NEUTRAL"}, {"SKIPPED"}}, domain.CIPassing, 3, 0, 0, 3},
		{"one failing", []row{{"SUCCESS"}, {"FAILURE"}}, domain.CIFailing, 1, 1, 0, 2},
		{"pending", []row{{"SUCCESS"}, {"IN_PROGRESS"}}, domain.CIPending, 1, 0, 1, 2},
		{"fail beats pending", []row{{"FAILURE"}, {"PENDING"}}, domain.CIFailing, 0, 1, 1, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := tally(c.rows)
			if got.State != c.want || got.Passed != c.pass || got.Failed != c.fail || got.Pending != c.pend || got.Total != c.total {
				t.Errorf("tally = %+v, want state=%s pass=%d fail=%d pend=%d total=%d",
					got, c.want, c.pass, c.fail, c.pend, c.total)
			}
		})
	}
}

func TestLastURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://github.com/o/r/pull/7", "https://github.com/o/r/pull/7"},
		{"Creating pull request\nhttps://github.com/o/r/pull/9", "https://github.com/o/r/pull/9"},
		{"no url here", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := lastURL(c.in); got != c.want {
			t.Errorf("lastURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
