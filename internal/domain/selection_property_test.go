package domain

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"testing/quick"
)

// selectionCase is a random but valid selection problem: base and head graphs
// over a shared universe of workspaces (so some are added or removed), random
// acyclic edges in each, and two batches of changed paths drawn from workspace
// files, unowned files, ignored files and the paths that force full.
type selectionCase struct {
	In    SelectionInput
	Extra []string
}

const propUniverse = 8

// propPath gives the universe a root workspace (0) and nested ones (6 inside
// 1, 7 inside 6), so ownership by longest prefix is exercised.
func propPath(i int) string {
	switch i {
	case 0:
		return "."
	case 6:
		return "pkg/w1/sub"
	case 7:
		return "pkg/w1/sub/deep"
	}
	return fmt.Sprintf("pkg/w%d", i)
}

func propFile(i, f int) string {
	if i == 0 {
		return fmt.Sprintf("f%d.ts", f)
	}
	return fmt.Sprintf("%s/f%d.ts", propPath(i), f)
}

func randomGraph(r *rand.Rand) WorkspaceGraph {
	var members []int
	for i := 0; i < propUniverse; i++ {
		if r.Intn(4) > 0 {
			members = append(members, i)
		}
	}
	var g WorkspaceGraph
	for k, i := range members {
		w := Workspace{Path: propPath(i), Name: fmt.Sprintf("w%d", i), Ecosystem: "js"}
		// Edges only point at earlier members, so the graph is acyclic.
		for _, j := range members[:k] {
			if r.Intn(3) == 0 {
				w.Deps = append(w.Deps, propPath(j))
			}
		}
		g.Workspaces = append(g.Workspaces, w)
	}
	return g
}

func randomPaths(r *rand.Rand, n int) []string {
	pool := []string{"Makefile", "docs/a.md", "docs/b/c.md", "pkg/README.md", "pkg/w1x/a"}
	var out []string
	for k := 0; k < n; k++ {
		if r.Intn(10) < 8 {
			out = append(out, propFile(r.Intn(propUniverse), r.Intn(3)))
		} else {
			out = append(out, pool[r.Intn(len(pool))])
		}
	}
	return out
}

func (selectionCase) Generate(r *rand.Rand, _ int) reflect.Value {
	in := SelectionInput{
		Base:                      randomGraph(r),
		Head:                      randomGraph(r),
		Changed:                   randomPaths(r, r.Intn(5)),
		AnchorConfigurationDigest: cfg, ConfigurationDigest: cfg,
	}
	if r.Intn(2) == 0 {
		in.Rules.Ignore = []string{"docs/**"}
	}
	if r.Intn(4) == 0 {
		in.Rules.Unowned = UnownedIgnore
	}
	return reflect.ValueOf(selectionCase{In: in, Extra: randomPaths(r, 1+r.Intn(4))})
}

var quickConfig = &quick.Config{MaxCount: 2000}

func selectedSet(s Selection) map[string]bool {
	out := map[string]bool{}
	for _, p := range s.Paths() {
		out[p] = true
	}
	return out
}

// Adding changed paths never removes a workspace from the selection.
func TestSelectPropertyMonotonic(t *testing.T) {
	prop := func(c selectionCase) bool {
		small, err := Select(c.In)
		if err != nil {
			t.Log(err)
			return false
		}
		bigger := c.In
		bigger.Changed = append(append([]string{}, c.In.Changed...), c.Extra...)
		big, err := Select(bigger)
		if err != nil {
			t.Log(err)
			return false
		}
		have := selectedSet(big)
		for p := range selectedSet(small) {
			if !have[p] {
				t.Logf("adding %v dropped %s: %v -> %v", c.Extra, p, small.Paths(), big.Paths())
				return false
			}
		}
		return true
	}
	if err := quick.Check(prop, quickConfig); err != nil {
		t.Fatal(err)
	}
}

// Any path that forces full yields exactly every head workspace, whatever
// else changed and whatever the ignore rules say.
func TestSelectPropertyForcingPathSelectsAll(t *testing.T) {
	forcing := append(append(append([]string{WardenConfigFile, "tools/ci/x"}, WorkspaceRootManifests...), RootLockfiles...), DefaultGlobalPaths[0], ".yarn/releases/y.cjs")
	prop := func(c selectionCase, pick uint8) bool {
		in := c.In
		in.Rules.Global = []string{"tools/**"}
		in.Changed = append(append([]string{}, in.Changed...), forcing[int(pick)%len(forcing)])
		s, err := Select(in)
		if err != nil {
			t.Log(err)
			return false
		}
		if s.Kind != SelectionFull || s.Full == nil || s.Full.Rule == FullRuleUnowned {
			t.Logf("%v: kind %s full %+v", in.Changed, s.Kind, s.Full)
			return false
		}
		return reflect.DeepEqual(s.Paths(), in.Head.Canonical().Paths())
	}
	if err := quick.Check(prop, quickConfig); err != nil {
		t.Fatal(err)
	}
}

// The selection is closed under reverse dependencies, over both graphs' edges:
// if a workspace (or a removed one) is affected, every head workspace that
// depends on it is selected. (A base-only dependent was removed: there is
// nothing left to run for it.)
func TestSelectPropertyClosedUnderDependents(t *testing.T) {
	prop := func(c selectionCase) bool {
		s, err := Select(c.In)
		if err != nil {
			t.Log(err)
			return false
		}
		affected := selectedSet(s)
		for _, r := range s.Removed {
			affected[r] = true
		}
		head := map[string]bool{}
		for _, p := range c.In.Head.Paths() {
			head[p] = true
		}
		for _, g := range []WorkspaceGraph{c.In.Base, c.In.Head} {
			for _, w := range g.Workspaces {
				for _, d := range w.Deps {
					if head[w.Path] && affected[d] && !affected[w.Path] {
						t.Logf("%s depends on affected %s but was not selected", w.Path, d)
						return false
					}
				}
			}
		}
		// Every selected workspace is in head and has a reason.
		for _, w := range s.Workspaces {
			if !head[w.Path] || len(w.Reasons) == 0 {
				return false
			}
		}
		return true
	}
	if err := quick.Check(prop, quickConfig); err != nil {
		t.Fatal(err)
	}
}

// Graph and selection digests are deterministic and do not depend on the
// order of workspaces, dependencies, changed paths or globs.
func TestSelectPropertyDigestsOrderIndependent(t *testing.T) {
	prop := func(c selectionCase, seed int64) bool {
		r := rand.New(rand.NewSource(seed))
		shuffled := c.In
		shuffled.Base = shuffleGraph(r, c.In.Base)
		shuffled.Head = shuffleGraph(r, c.In.Head)
		shuffled.Changed = append(append([]string{}, c.In.Changed...), c.In.Changed...)
		r.Shuffle(len(shuffled.Changed), func(i, j int) {
			shuffled.Changed[i], shuffled.Changed[j] = shuffled.Changed[j], shuffled.Changed[i]
		})
		for _, pair := range [][2]WorkspaceGraph{{c.In.Base, shuffled.Base}, {c.In.Head, shuffled.Head}} {
			a, errA := pair[0].Digest()
			b, errB := pair[1].Digest()
			again, _ := pair[0].Digest()
			if errA != nil || errB != nil || a != b || a != again {
				t.Logf("graph digests %s %s %s (%v %v)", a, b, again, errA, errB)
				return false
			}
		}
		s1, err1 := Select(c.In)
		s2, err2 := Select(shuffled)
		if err1 != nil || err2 != nil {
			return false
		}
		d1, _ := s1.Digest()
		d2, _ := s2.Digest()
		return d1 == d2 && reflect.DeepEqual(s1, s2)
	}
	if err := quick.Check(prop, quickConfig); err != nil {
		t.Fatal(err)
	}
}

func shuffleGraph(r *rand.Rand, g WorkspaceGraph) WorkspaceGraph {
	out := make([]Workspace, len(g.Workspaces))
	for i, w := range g.Workspaces {
		w.Deps = append(append([]string{}, w.Deps...), w.Deps...) // duplicates too
		r.Shuffle(len(w.Deps), func(a, b int) { w.Deps[a], w.Deps[b] = w.Deps[b], w.Deps[a] })
		out[i] = w
	}
	r.Shuffle(len(out), func(a, b int) { out[a], out[b] = out[b], out[a] })
	return WorkspaceGraph{Workspaces: out}
}
