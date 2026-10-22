package steps

import (
	"bytes"

	"go.klarlabs.de/warden/internal/domain"
)

const maxObservationBytes = 4096

// extractObservation accepts only the first complete, bounded probe frame.
// The trusted image shell emits it before executing repository commands; later
// output (including a second frame) cannot amend this initial observation.
func extractObservation(out []byte, begin, end string) ([]byte, *domain.ContainerObservation) {
	startMarker, endMarker := []byte(begin+"\n"), []byte(end+"\n")
	start := bytes.Index(out, startMarker)
	if start < 0 || (start > 0 && out[start-1] != '\n') {
		return out, nil
	}
	body := start + len(startMarker)
	tail := bytes.Index(out[body:], endMarker)
	if tail < 0 {
		return out, nil
	}
	finish := body + tail
	clean := append(append([]byte(nil), out[:start]...), out[finish+len(endMarker):]...)
	if tail > maxObservationBytes || (tail > 0 && out[finish-1] != '\n') {
		return clean, nil
	}
	observation := domain.ParseContainerObservation(string(out[body:finish]))
	return clean, &observation
}

// probeCapture bounds the separate stdout copy. The engine is expected to emit
// the probe first; an unexpectedly large prefix leaves the evidence absent.
type probeCapture struct{ bytes.Buffer }

func (p *probeCapture) Write(data []byte) (int, error) {
	n := len(data)
	remaining := maxObservationBytes + 1024 - p.Len()
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		_, _ = p.Buffer.Write(data)
	}
	return n, nil
}

// Strip only the exact stdout frame. If stderr interleaved, preserve all
// diagnostics rather than accidentally removing a workload's error message.
func stripObservationFrame(out, stdout []byte, begin, end string) []byte {
	start := bytes.Index(stdout, []byte(begin+"\n"))
	if start < 0 {
		return out
	}
	tail := bytes.Index(stdout[start:], []byte(end+"\n"))
	if tail < 0 {
		return out
	}
	frame := stdout[start : start+tail+len(end)+1]
	return bytes.Replace(out, frame, nil, 1)
}
