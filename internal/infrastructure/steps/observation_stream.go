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
