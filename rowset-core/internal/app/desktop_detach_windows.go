//go:build windows

package app

import "syscall"

// CREATE_NEW_PROCESS_GROUP so the child does not receive Ctrl+C/Ctrl+Break
// meant for the parent's console, and DETACHED_PROCESS so it never
// allocates a console of its own (no window ever flashes, and closing the
// parent's console does not touch it).
const (
	createNewProcessGroup = 0x00000200
	detachedProcess       = 0x00000008
)

func detachedAttrs() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: createNewProcessGroup | detachedProcess}
}
