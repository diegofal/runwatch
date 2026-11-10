//go:build ignore

// fmtcheck fails when gofmt would change any Go file under the given folders.
// It exists so `task fmt-check` behaves the same on every OS.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func main() {
	var dirs []string
	for _, d := range os.Args[1:] {
		if _, err := os.Stat(d); err == nil {
			dirs = append(dirs, d)
		}
	}
	out, err := exec.Command("gofmt", append([]string{"-l"}, dirs...)...).Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, "gofmt:", err)
		os.Exit(2)
	}
	if files := strings.TrimSpace(string(out)); files != "" {
		fmt.Fprintln(os.Stderr, "not gofmt'ed:\n"+files)
		os.Exit(1)
	}
}
