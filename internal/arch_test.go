package internal_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const module = "runwatch"

// layer describes one group of packages: which internal packages it may
// import and whether it may import modules outside the standard library.
type layer struct {
	name     string
	match    func(rel string) bool
	internal []string // allowed internal prefixes (relative to the module)
	external []string // allowed external module prefixes; "*" allows any
}

func under(prefixes ...string) func(string) bool {
	return func(rel string) bool {
		for _, p := range prefixes {
			if rel == p || strings.HasPrefix(rel, p+"/") {
				return true
			}
		}
		return false
	}
}

var layers = []layer{
	{name: "domain", match: under("internal/domain")},
	{name: "core", match: under("internal/core"), internal: []string{"internal/domain", "internal/core"}},
	{name: "fake GitHub", match: under("internal/fakegh")},
	{name: "config", match: under("internal/config")},
	{name: "web files", match: under("web")},
	{name: "adapters", match: under("internal/adapters"), internal: []string{"internal/domain", "internal/adapters"}, external: []string{"*"}},
	// The server maps GitHub's refusals to an HTTP status, so it knows that one error type.
	{name: "server", match: under("internal/server"), internal: []string{"internal/domain", "internal/core", "internal/adapters/github"}},
	{name: "tray", match: under("internal/tray"), internal: []string{"internal/domain", "internal/core", "internal/tray"}, external: []string{"*"}},
	{name: "wiring", match: under("cmd", "internal/wiring"), internal: []string{""}, external: []string{"*"}},
	{name: "end-to-end tests", match: under("e2e")},
	{name: "arch test", match: func(rel string) bool { return rel == "internal" }},
}

func isStd(imp string) bool {
	first := strings.SplitN(imp, "/", 2)[0]
	return !strings.Contains(first, ".")
}

func allowed(prefixes []string, s string) bool {
	for _, p := range prefixes {
		if p == "*" || p == "" || s == p || strings.HasPrefix(s, p+"/") {
			return true
		}
	}
	return false
}

// check returns the rule violations of one package.
func check(pkg string, imports []string) []string {
	rel := strings.TrimPrefix(strings.TrimPrefix(pkg, module), "/")
	var l *layer
	for i := range layers {
		if layers[i].match(rel) {
			l = &layers[i]
			break
		}
	}
	if l == nil {
		return []string{pkg + ": belongs to no layer; add it to internal/arch_test.go"}
	}
	var bad []string
	for _, imp := range imports {
		switch {
		case imp == module || strings.HasPrefix(imp, module+"/"):
			target := strings.TrimPrefix(strings.TrimPrefix(imp, module), "/")
			if !allowed(l.internal, target) {
				bad = append(bad, rel+" ("+l.name+") must not import "+target)
			}
		case isStd(imp):
		default:
			if !allowed(l.external, imp) {
				bad = append(bad, rel+" ("+l.name+") must not import external "+imp)
			}
		}
	}
	return bad
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestArchitecture(t *testing.T) {
	cmd := exec.Command("go", "list", "-f", "{{.ImportPath}}|{{join .Imports \",\"}}", "./...")
	cmd.Dir = repoRoot(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		pkg, imps, _ := strings.Cut(strings.TrimSpace(line), "|")
		var imports []string
		if imps != "" {
			imports = strings.Split(imps, ",")
		}
		for _, v := range check(pkg, imports) {
			t.Error(v)
		}
	}
}

func TestArchitectureCatchesViolations(t *testing.T) {
	bad := []struct {
		pkg     string
		imports []string
	}{
		{module + "/internal/domain", []string{module + "/internal/core"}},
		{module + "/internal/domain", []string{"github.com/some/lib"}},
		{module + "/internal/core", []string{module + "/internal/adapters/github"}},
		{module + "/internal/core", []string{"github.com/some/lib"}},
		{module + "/internal/adapters/github", []string{module + "/internal/core"}},
		{module + "/internal/adapters/runnerfs", []string{module + "/internal/fakegh"}},
		{module + "/internal/server", []string{module + "/internal/adapters/winsvc"}},
		{module + "/internal/server", []string{"github.com/some/lib"}},
		{module + "/internal/tray", []string{module + "/internal/adapters/github"}},
		{module + "/internal/fakegh", []string{module + "/internal/domain"}},
		{module + "/internal/newthing", nil},
	}
	for _, c := range bad {
		if len(check(c.pkg, c.imports)) == 0 {
			t.Errorf("%s importing %v should violate the rules", c.pkg, c.imports)
		}
	}
	ok := []struct {
		pkg     string
		imports []string
	}{
		{module + "/internal/domain", []string{"encoding/json", "time"}},
		{module + "/internal/core", []string{module + "/internal/domain", "context"}},
		{module + "/internal/adapters/winsvc", []string{"golang.org/x/sys/windows", module + "/internal/adapters/execx", module + "/internal/domain"}},
		{module + "/internal/server", []string{module + "/internal/core", module + "/internal/adapters/github", "net/http"}},
		{module + "/internal/tray/shell", []string{"github.com/wailsapp/wails/v3/pkg/application", module + "/internal/core", module + "/internal/tray"}},
		{module + "/cmd/runwatch", []string{module + "/internal/wiring", module + "/internal/adapters/notify"}},
	}
	for _, c := range ok {
		if v := check(c.pkg, c.imports); len(v) != 0 {
			t.Errorf("%s should be allowed: %v", c.pkg, v)
		}
	}
}
