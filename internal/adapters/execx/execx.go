// Package execx is the one place runwatch starts programs
// (internal/arch_test.go). Run starts them unseen: the tray has no console,
// so on Windows every child process would otherwise flash a window that
// takes the focus. RunInteractive is for the one program the user talks to,
// none today.
package execx

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// maxOutput bounds how much command output goes into an error.
const maxOutput = 1024

// Command is exec.CommandContext with Background applied.
func Command(ctx context.Context, name string, args ...string) *exec.Cmd {
	return Background(exec.CommandContext(ctx, name, args...))
}

// Run runs name with args in dir ("" = the current folder), with env
// ("KEY=value" entries) added to this process's environment, and returns its
// combined output as printed. When the command fails, the error wraps the
// exec error and carries the output as Text, cut to 1 KiB.
func Run(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, error) {
	cmd := Command(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = environ(env)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := Text(out)
		if len(msg) > maxOutput {
			msg = msg[:maxOutput] + "…"
		}
		return out, fmt.Errorf("%s: %w: %s", name, err, msg)
	}
	return out, nil
}

// RunInteractive runs name with args attached to this process's terminal
// (stdin, stdout, stderr), env added as in Run, and waits for it. It opens
// no hidden console: the user has to see and answer it.
func RunInteractive(ctx context.Context, env []string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = environ(env)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// Text is output as people read it: escape sequences stripped, trimmed.
func Text(out []byte) string { return strings.TrimSpace(stripANSI(string(out))) }

// environ is nil (inherit) when env is empty, else this process's
// environment with env after it, so env wins.
func environ(env []string) []string {
	if len(env) == 0 {
		return nil
	}
	return append(os.Environ(), env...)
}
