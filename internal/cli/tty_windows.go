//go:build windows

package cli

import (
	"errors"
	"io"
)

// openControllingTTY is not implemented on Windows: a hook there keeps the
// earlier behavior and declines when stdin is not a terminal.
func openControllingTTY() (io.ReadWriteCloser, error) {
	return nil, errors.New("no controlling terminal support on windows")
}
