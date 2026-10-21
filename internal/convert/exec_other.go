//go:build !windows

package convert

import "os/exec"

func hideWindow(cmd *exec.Cmd) {}
