package steps

import (
	"bytes"
	"io"
	"os/exec"

	"go.klarlabs.de/warden/internal/application"
)

// lineWriter splits the bytes written to it into lines and hands each complete
// line to onLine, so a running command's output can be streamed to a live UI as
// it arrives. A trailing partial line is flushed by Close. It is not safe for
// concurrent writes, but exec wires a command's stdout+stderr to it serially.
type lineWriter struct {
	onLine func(string)
	buf    []byte
}

func newLineWriter(onLine func(string)) *lineWriter { return &lineWriter{onLine: onLine} }

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.onLine(string(w.buf[:i]))
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}

// Close flushes any buffered partial final line (a command that doesn't end its
// last line with a newline).
func (w *lineWriter) Close() {
	if len(w.buf) > 0 {
		w.onLine(string(w.buf))
		w.buf = nil
	}
}

// runCaptured runs cmd and returns its combined stdout+stderr and exit error.
// When sc.OnOutput is set it also streams each line live; otherwise it takes the
// plain buffered path, so the non-interactive pipeline pays nothing.
func runCaptured(cmd *exec.Cmd, sc application.StepContext) ([]byte, error) {
	if sc.OnOutput == nil {
		return cmd.CombinedOutput()
	}
	var buf bytes.Buffer
	lw := newLineWriter(sc.OnOutput)
	w := io.MultiWriter(&buf, lw)
	cmd.Stdout = w
	cmd.Stderr = w
	err := cmd.Run()
	lw.Close()
	return buf.Bytes(), err
}
