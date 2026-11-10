package machine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"runwatch/internal/adapters/winsvc"
	"runwatch/internal/domain"
	"runwatch/internal/fakegh"
)

func TestRunnersFromFoldersAndServices(t *testing.T) {
	root, elsewhere := t.TempDir(), t.TempDir()
	w := fakegh.New(root)
	for _, x := range [][2]string{{"example/app", "app-2"}, {"example/app", "app-1"}, {"acme/billing", "billing-1"}} {
		if _, err := w.AddRunner(x[0], x[1]); err != nil {
			t.Fatal(err)
		}
	}
	// A runner installed as a service outside the configured folders.
	far, err := fakegh.New(elsewhere).AddRunner("example/hooks", "hooks-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app-2", ".runner"), []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := &Machine{Roots: []string{root}, Services: func() ([]winsvc.Service, error) {
		return []winsvc.Service{
			{Name: "actions.runner.hooks-1", Dir: far.Dir, State: domain.ServiceStopped},
			// The registry and the folder listing may differ in case and slashes.
			{Name: "actions.runner.app-1", Dir: strings.ToUpper(filepath.Join(root, "app-1")) + string(filepath.Separator), State: domain.ServiceRunning},
		}, nil
	}}
	runners, err := m.Runners(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range runners {
		got = append(got, r.Repo+" "+r.Name+" "+string(r.State)+" "+r.Service)
	}
	want := []string{
		" app-2 unknown ", // unreadable: no repo, still listed, sorted first
		"acme/billing billing-1 unknown ",
		"example/app app-1 running actions.runner.app-1",
		"example/hooks hooks-1 stopped actions.runner.hooks-1",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("runners =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if !strings.Contains(runners[0].Problem, "parse .runner") {
		t.Errorf("the unreadable runner must say why: %+v", runners[0])
	}
}

func TestRunnersReportsWhatItCouldNotList(t *testing.T) {
	root := t.TempDir()
	if _, err := fakegh.New(root).AddRunner("example/app", "app-1"); err != nil {
		t.Fatal(err)
	}
	m := &Machine{Roots: []string{root, filepath.Join(root, "missing")},
		Services: func() ([]winsvc.Service, error) { return nil, errors.New("service manager: access denied") }}
	runners, err := m.Runners(context.Background())
	if len(runners) != 1 {
		t.Errorf("what can be read is still returned: %+v", runners)
	}
	if err == nil || !strings.Contains(err.Error(), "access denied") || !strings.Contains(err.Error(), "missing") {
		t.Errorf("err = %v; want both failures", err)
	}
}

func TestControl(t *testing.T) {
	var got string
	m := &Machine{ControlFn: func(_ context.Context, service string, start bool) error {
		got = service
		if !start {
			return errors.New("declined")
		}
		return nil
	}}
	if err := m.Control(context.Background(), "actions.runner.app-1", true); err != nil || got != "actions.runner.app-1" {
		t.Errorf("start: %v, %q", err, got)
	}
	if err := m.Control(context.Background(), "actions.runner.app-1", false); err == nil {
		t.Error("a declined admin prompt must be an error")
	}
	if err := (&Machine{}).Control(context.Background(), "x", true); err == nil {
		t.Error("no control function: want an error")
	}
	if New([]string{"x"}).Services == nil {
		t.Error("New must wire the OS's service reader")
	}
}
