//go:build windows

package convert

import (
	"os/exec"
	"syscall"
)

// hideWindow keeps console encoders from flashing a window.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
