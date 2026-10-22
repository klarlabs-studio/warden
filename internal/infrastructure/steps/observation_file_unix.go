//go:build unix

package steps

import "syscall"

const observationNonblock = syscall.O_NONBLOCK | syscall.O_NOFOLLOW
