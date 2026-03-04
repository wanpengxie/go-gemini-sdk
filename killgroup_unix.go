//go:build !windows

package gemini

import "syscall"

func killProcessGroup(pid int) error {
	if pid <= 0 {
		return nil
	}
	return syscall.Kill(-pid, syscall.SIGKILL)
}
