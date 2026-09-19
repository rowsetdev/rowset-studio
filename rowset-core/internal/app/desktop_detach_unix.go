//go:build !windows

package app

import "syscall"

// Starts the child in its own session, detached from the parent's
// controlling terminal, so closing that terminal does not send it a
// hangup.
func detachedAttrs() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
