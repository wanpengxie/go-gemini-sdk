//go:build windows

package gemini

func killProcessGroup(pid int) error {
	_ = pid
	return nil
}
