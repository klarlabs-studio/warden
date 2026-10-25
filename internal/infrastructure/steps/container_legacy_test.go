package steps

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const legacyNonce = "0123456789abcdef0123456789abcdef"

func TestIsLegacyName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"warden-" + legacyNonce, true},
		{"warden-4242-" + legacyNonce, false},             // current scheme: owner pid in the name
		{"warden-" + strings.ToUpper(legacyNonce), false}, // not a nonce warden gave
		{"warden-" + legacyNonce[:31], false},
		{"warden-" + legacyNonce + "0", false},
		{"warden-db", false},
		{"my-warden-" + legacyNonce, false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isLegacyName(tt.name); got != tt.want {
			t.Errorf("isLegacyName(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestLegacyCompatCandidates(t *testing.T) {
	listing := strings.Join([]string{
		"warden-" + legacyNonce + "\t",                                              // legacy, docker: no labels
		"warden-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\tmap[]",                            // legacy, podman: empty map
		"warden-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\tcom.example=1",                    // legacy, foreign label only
		"warden-cccccccccccccccccccccccccccccccc\tde.klarlabs.warden.machine=m1",    // labeled: a current warden's
		"warden-dddddddddddddddddddddddddddddddd\tmap[de.klarlabs.warden.host:mbp]", // old host label: still warden's
		"warden-4242-" + legacyNonce + "\tde.klarlabs.warden.pid=4242",              // current scheme
		"warden-db\t", // someone else's
		"",
	}, "\n")
	got := legacyCompatCandidates([]byte(listing))
	want := []string{
		"warden-" + legacyNonce,
		"warden-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"warden-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("candidates = %v, want %v", got, want)
	}
}

func TestLegacyAppleCandidates(t *testing.T) {
	listing := `[
	 {"status":{"state":"stopped"},"configuration":{"id":"warden-` + legacyNonce + `","labels":{}}},
	 {"status":"stopped","configuration":{"id":"warden-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}},
	 {"status":{"state":"running"},"configuration":{"id":"warden-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}},
	 {"configuration":{"id":"warden-eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"}},
	 {"status":{"state":"stopped"},"configuration":{"id":"warden-cccccccccccccccccccccccccccccccc","labels":{"de.klarlabs.warden.machine":"m1"}}},
	 {"status":{"state":"stopped"},"configuration":{"id":"warden-4242-` + legacyNonce + `"}},
	 {"status":{"state":"stopped"},"configuration":{"id":"buildkit"}}
	]`
	got := legacyAppleCandidates([]byte(listing))
	want := []string{"warden-" + legacyNonce, "warden-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("candidates = %v, want %v", got, want)
	}
	if got := legacyAppleCandidates([]byte("not json")); got != nil {
		t.Errorf("unparseable listing gave %v, want nothing", got)
	}
}

// fakeEngine scripts an engine CLI: the listing it answers, the containers
// it refuses to remove, and every call it received.
type fakeEngine struct {
	listing []byte
	listErr error
	refuse  map[string]bool
	calls   [][]string
}

func (e *fakeEngine) call(args ...string) ([]byte, error) {
	e.calls = append(e.calls, args)
	switch args[0] {
	case "ps", "list":
		return e.listing, e.listErr
	default:
		if e.refuse[args[len(args)-1]] {
			return nil, errors.New("container is running")
		}
		return nil, nil
	}
}

func TestRemoveLegacy(t *testing.T) {
	stale := "warden-" + legacyNonce
	refused := "warden-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	t.Run("docker-compatible", func(t *testing.T) {
		e := &fakeEngine{
			listing: []byte(stale + "\t\n" + refused + "\t\nwarden-cccccccccccccccccccccccccccccccc\tde.klarlabs.warden.pid=1\n"),
			refuse:  map[string]bool{refused: true},
		}
		res := removeLegacy("docker", e.call)
		if res.Err != nil || !reflect.DeepEqual(res.Removed, []string{stale}) || !reflect.DeepEqual(res.Failed, []string{refused}) {
			t.Fatalf("result = %+v", res)
		}
		list := e.calls[0]
		for _, filter := range []string{"status=exited", "status=created", "name=warden-"} {
			if !slices.Contains(list, filter) {
				t.Errorf("listing %v lacks filter %s: a running container could be listed", list, filter)
			}
		}
		for _, c := range e.calls[1:] {
			if c[0] != "rm" || slices.Contains(c, "--force") || slices.Contains(c, "-f") {
				t.Errorf("removal %v must be an unforced rm, so the engine refuses a running container", c)
			}
		}
		if len(e.calls) != 3 {
			t.Errorf("calls = %v, want one listing and two removals (never the labeled container)", e.calls)
		}
	})
	t.Run("apple", func(t *testing.T) {
		e := &fakeEngine{listing: []byte(`[{"status":{"state":"stopped"},"configuration":{"id":"` + stale + `"}}]`)}
		res := removeLegacy("apple", e.call)
		if res.Err != nil || !reflect.DeepEqual(res.Removed, []string{stale}) {
			t.Fatalf("result = %+v", res)
		}
		if got := e.calls[1]; !reflect.DeepEqual(got, []string{"delete", stale}) {
			t.Errorf("removal = %v, want an unforced delete", got)
		}
	})
	t.Run("listing fails", func(t *testing.T) {
		for _, engine := range []string{"podman", "apple"} {
			e := &fakeEngine{listErr: errors.New("cannot connect to the engine")}
			res := removeLegacy(engine, e.call)
			if res.Err == nil || len(res.Removed) != 0 || len(e.calls) != 1 {
				t.Errorf("%s: result = %+v, calls = %v; want the error and nothing removed", engine, res, e.calls)
			}
		}
	})
}
