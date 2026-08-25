package proc

import (
	"runtime"
	"strings"
	"testing"
)

// The distinction the whole thing rests on. Windows exposes no load average, and
// a caller must never read that as an idle machine — a zero would silently
// suppress the caveat this exists to print, on exactly the platform where
// nobody would notice it had gone missing.
func TestLoad_UnknownIsNotIdle(t *testing.T) {
	// Value is deliberately non-zero. A zero would make this pass whether or
	// not the Known guard exists — mutation-checking caught exactly that: the
	// first version of this test used the default 0 and survived removing the
	// guard, so it was asserting arithmetic rather than the property.
	unknown := Load{Known: false, Value: 109, CPUs: 16}
	if unknown.Ratio() != 0 {
		t.Errorf("Ratio = %v, want 0 when unknown", unknown.Ratio())
	}
	if unknown.Contended() {
		t.Error("an unknown load must never report contention — it has measured nothing")
	}
	if !strings.Contains(unknown.String(), "unknown") {
		t.Errorf("String = %q, must say it does not know", unknown.String())
	}
	// And an idle machine, which is a real measurement, must read differently.
	idle := Load{Known: true, Value: 0.1, CPUs: 16}
	if strings.Contains(idle.String(), "unknown") {
		t.Errorf("a measured idle machine must not read as unknown: %q", idle.String())
	}
}

// Load is only meaningful per core: 8 is unremarkable on 16 and severe on 2.
// A threshold on the raw figure would fire constantly on small machines and
// never on large ones.
func TestLoad_RatioIsPerCore(t *testing.T) {
	cases := []struct {
		name      string
		load      Load
		ratio     float64
		contended bool
	}{
		{"idle 16-core", Load{Known: true, Value: 1, CPUs: 16}, 0.0625, false},
		{"busy but fine", Load{Known: true, Value: 30, CPUs: 10}, 3, false},
		{"at the threshold", Load{Known: true, Value: 40, CPUs: 10}, 4, true},
		// The situation in #249: load 109 on 10 cores.
		{"the reported case", Load{Known: true, Value: 109, CPUs: 10}, 10.9, true},
		// The same absolute load on a big machine is not contention.
		{"8 on 2 cores", Load{Known: true, Value: 8, CPUs: 2}, 4, true},
		{"8 on 64 cores", Load{Known: true, Value: 8, CPUs: 64}, 0.125, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.load.Ratio(); got < c.ratio-0.01 || got > c.ratio+0.01 {
				t.Errorf("Ratio = %v, want ~%v", got, c.ratio)
			}
			if got := c.load.Contended(); got != c.contended {
				t.Errorf("Contended = %v, want %v (ratio %v)", got, c.contended, c.load.Ratio())
			}
		})
	}
}

// A zero or negative core count must not divide. It cannot happen through
// CurrentLoad, but a zero Load{} is the natural value for a caller that never
// measured, and it must be inert rather than panicking.
func TestLoad_ZeroValueIsInert(t *testing.T) {
	var l Load
	if l.Ratio() != 0 || l.Contended() {
		t.Errorf("the zero Load must measure nothing, got ratio %v contended %v", l.Ratio(), l.Contended())
	}
	if l.String() == "" {
		t.Error("even the zero value must render something a report can print")
	}
}

// The real reader, on whatever platform the suite runs. It must either produce
// a usable measurement or say it cannot — never a confident zero.
func TestCurrentLoad_MeasuresOrSaysItCannot(t *testing.T) {
	l := CurrentLoad()
	if l.CPUs != runtime.NumCPU() {
		t.Errorf("CPUs = %d, want %d", l.CPUs, runtime.NumCPU())
	}
	switch runtime.GOOS {
	case "linux", "darwin", "freebsd", "openbsd", "netbsd":
		if !l.Known {
			t.Errorf("%s exposes a load average; CurrentLoad reported none", runtime.GOOS)
		}
		if l.Value < 0 {
			t.Errorf("negative load %v", l.Value)
		}
	default:
		// Elsewhere either answer is acceptable; what is not acceptable is
		// claiming to know a value of zero.
		if l.Known && l.Value == 0 {
			t.Error("a load of exactly 0 is more likely a failed read than an idle machine")
		}
	}
}
