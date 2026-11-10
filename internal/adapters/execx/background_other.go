//go:build !windows

package execx

import "os/exec"

// Background returns cmd as it is: elsewhere a program started without a
// terminal opens no window.
func Background(cmd *exec.Cmd) *exec.Cmd { return cmd }
