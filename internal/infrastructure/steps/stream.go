package steps

import (
	"bytes"
	"io"
	"os/exec"
	"sync"

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
	return runCapturedStdout(cmd, sc, nil, nil)
}

// runCapturedStdout additionally preserves stdout independently of stderr.
// Probe evidence must use this stream: exec may read stderr before earlier
// stdout bytes when separate engine pipes are ready concurrently.
func runCapturedStdout(cmd *exec.Cmd, sc application.StepContext, stdout io.Writer, stdoutLine func(string)) ([]byte, error) {
	if sc.OnOutput == nil && stdout == nil {
		return cmd.CombinedOutput()
	}
	var buf bytes.Buffer
	var stdoutWriter, stderrWriter io.Writer = &buf, &buf
	var stdoutLines, stderrLines *lineWriter
	if sc.OnOutput != nil {
		if stdoutLine == nil {
			stdoutLine = sc.OnOutput
		}
		stdoutLines, stderrLines = newLineWriter(stdoutLine), newLineWriter(sc.OnOutput)
		stdoutWriter, stderrWriter = io.MultiWriter(&buf, stdoutLines), io.MultiWriter(&buf, stderrLines)
	}
	var mu sync.Mutex
	cmd.Stdout = &lockedWriter{mu: &mu, writer: stdoutWriter}
	cmd.Stderr = &lockedWriter{mu: &mu, writer: stderrWriter}
	if stdout != nil {
		cmd.Stdout = io.MultiWriter(cmd.Stdout, stdout)
	}
	err := cmd.Run()
	if stdoutLines != nil {
		stdoutLines.Close()
		stderrLines.Close()
	}
	return buf.Bytes(), err
}

type lockedWriter struct {
	mu     *sync.Mutex
	writer io.Writer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(p)
}
