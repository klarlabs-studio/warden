package proc

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// Load is the machine's one-minute run-queue length and how many cores it has
// to service it.
//
// WHY WARDEN CARES. A step that fails on a starved machine is reported exactly
// like a step that fails because the code is broken: both are a non-zero exit,
// and the verdict a developer reads — "step test failed" — is a claim about
// their change. Warden could not previously tell those apart, and worse, never
// collected the one piece of context that would let a HUMAN tell them apart.
// (klarlabs-studio/warden#249: a suite that fits a 10-minute budget on an idle
// box hit it at load 109 on 10 cores, and the author was told their commit had
// been rejected.)
//
// This does not decide anything. A deadlock under load is still a deadlock, and
// inferring "the machine was busy" from a timeout would be the same over-claim
// in the other direction. Load is context reported BESIDE a verdict, never a
// substitute for one.
type Load struct {
	// Known is false when the platform exposes no load average — Windows has no
	// equivalent, and a caller must not read Value as "idle" there.
	Known bool
	// Value is the 1-minute load average.
	Value float64
	// CPUs is what that load is spread across; the ratio is what matters, not
	// the number. Load 8 is unremarkable on 16 cores and severe on 2.
	CPUs int
}

// Ratio is load per core, the comparable figure. Zero when unknown.
func (l Load) Ratio() float64 {
	if !l.Known || l.CPUs <= 0 {
		return 0
	}
	return l.Value / float64(l.CPUs)
}

// Contended reports whether the machine was oversubscribed enough that a
// wall-clock failure deserves a caveat.
//
// The threshold is deliberately high. At 4x the core count a process gets
// roughly a quarter of the CPU it would idle, which turns a 3-minute package
// into 12 and blows a 10-minute budget — the situation in #249 was 10.9x. Below
// that, mentioning load would add noise to failures that are about the code,
// and a caveat printed on every failure is a caveat nobody reads.
func (l Load) Contended() bool { return l.Ratio() >= 4 }

// String renders the measurement, never a conclusion.
func (l Load) String() string {
	if !l.Known {
		return "load unknown on this platform"
	}
	return strconv.FormatFloat(l.Value, 'f', 2, 64) + " on " + strconv.Itoa(l.CPUs) + " core(s)"
}

// CurrentLoad reads the 1-minute load average.
//
// Linux exposes it in /proc/loadavg; the BSDs and macOS through
// `sysctl -n vm.loadavg`. Go's standard library offers no portable accessor and
// warden will not add a cgo dependency for a diagnostic, so both are read
// directly and anything else reports UNKNOWN rather than zero. A zero would be
// indistinguishable from an idle machine and would silently suppress the very
// caveat this exists to print.
func CurrentLoad() Load {
	l := Load{CPUs: runtime.NumCPU()}
	if v, ok := loadFromProc(); ok {
		l.Known, l.Value = true, v
		return l
	}
	if v, ok := loadFromSysctl(); ok {
		l.Known, l.Value = true, v
		return l
	}
	return l
}

// loadFromProc reads Linux's /proc/loadavg: "0.42 0.53 0.60 1/512 12345".
func loadFromProc() (float64, bool) {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	return v, err == nil
}

// loadFromSysctl reads the BSD/macOS form: "{ 4.73 3.89 7.60 }".
//
// Shelling out for a diagnostic is acceptable here and bounded: it runs once
// per step boundary, and a missing or slow sysctl degrades to UNKNOWN rather
// than delaying the gate.
func loadFromSysctl() (float64, bool) {
	bin, err := exec.LookPath("sysctl")
	if err != nil {
		return 0, false
	}
	out, err := exec.Command(bin, "-n", "vm.loadavg").Output()
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(strings.Trim(strings.TrimSpace(string(out)), "{}"))
	if len(fields) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	return v, err == nil
}
