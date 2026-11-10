package execx

import (
	"os/exec"
	"syscall"
)

// createNoWindow gives a console program a console without a window.
const createNoWindow = 0x08000000

// Background makes cmd run unseen, keeping whatever SysProcAttr it already
// has, and returns it.
func Background(cmd *exec.Cmd) *exec.Cmd {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
	return cmd
}
