//go:build ignore

// covergate fails when a package's statement coverage is under its gate.
// Usage: go run scripts/covergate.go coverage.out
package main

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

const module = "runwatch/"

// gate returns the minimum coverage for a package, or -1 to skip it.
// Skipped: wiring (cmd/ and internal/wiring, covered by the end-to-end
// tests, which run the built program), the fake GitHub (test equipment),
// the embedded web files, and the adapters that only talk to the desktop or
// the OS (notify, execx, winsvc, the tray's Wails shell).
func gate(pkg string) float64 {
	rel := strings.TrimPrefix(pkg, module)
	switch {
	case strings.HasPrefix(rel, "cmd/"), strings.HasPrefix(rel, "internal/wiring"), strings.HasPrefix(rel, "internal/fakegh"),
		strings.HasPrefix(rel, "internal/tray/shell"), strings.HasPrefix(rel, "internal/adapters/notify"),
		strings.HasPrefix(rel, "internal/adapters/execx"), strings.HasPrefix(rel, "internal/adapters/winsvc"),
		rel == "web", rel == "e2e":
		return -1
	case strings.HasPrefix(rel, "internal/domain"):
		return 90
	}
	return 85
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: covergate coverage.out")
		os.Exit(2)
	}
	f, err := os.Open(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	defer f.Close()
	// With -coverpkg every test binary lists every block: a block counts
	// once, covered when any listing ran it.
	type block struct {
		n       int
		covered bool
	}
	blocks := map[string]block{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "mode:") {
			continue
		}
		// file.go:1.2,3.4 statements count
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		n, _ := strconv.Atoi(fields[1])
		c, _ := strconv.Atoi(fields[2])
		b := blocks[fields[0]]
		blocks[fields[0]] = block{n: n, covered: b.covered || c > 0}
	}
	total, covered := map[string]int{}, map[string]int{}
	for pos, b := range blocks {
		file := pos[:strings.LastIndex(pos, ":")]
		pkg := file[:strings.LastIndex(file, "/")]
		total[pkg] += b.n
		if b.covered {
			covered[pkg] += b.n
		}
	}
	pkgs := make([]string, 0, len(total))
	for p := range total {
		pkgs = append(pkgs, p)
	}
	sort.Strings(pkgs)
	failed := false
	for _, p := range pkgs {
		g := gate(p)
		if g < 0 || total[p] == 0 {
			continue
		}
		pct := 100 * float64(covered[p]) / float64(total[p])
		mark := "ok  "
		if pct < g {
			mark, failed = "FAIL", true
		}
		fmt.Printf("%s %5.1f%% (gate %2.0f%%) %s\n", mark, pct, g, strings.TrimPrefix(p, module))
	}
	if failed {
		os.Exit(1)
	}
}
