// Package machine is core.Machine: the runners installed on this PC, found
// through their Windows services and the configured folders, each read from
// its own files.
package machine

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"strings"

	"runwatch/internal/adapters/runnerfs"
	"runwatch/internal/adapters/winsvc"
	"runwatch/internal/domain"
)

// Machine reads the runners under Roots plus those installed as services.
type Machine struct {
	// Roots are folders holding runner folders (or runner folders themselves).
	Roots []string
	// Services lists the runner services; nil = none (tests, other OSes).
	Services func() ([]winsvc.Service, error)
	// ControlFn starts or stops a service.
	ControlFn func(ctx context.Context, service string, start bool) error
}

// New returns the machine reader for this OS.
func New(roots []string) *Machine {
	return &Machine{Roots: roots, Services: winsvc.List, ControlFn: winsvc.Control}
}

// Runners returns every runner found, sorted by repo then name. A runner
// whose files cannot be read is returned with Problem set; the error is for
// failures that are not one runner's (a root that cannot be listed, the
// service manager).
func (m *Machine) Runners(context.Context) ([]domain.Runner, error) {
	var errs []error
	var services []winsvc.Service
	if m.Services != nil {
		var err error
		if services, err = m.Services(); err != nil {
			errs = append(errs, err)
		}
	}
	byDir := map[string]winsvc.Service{}
	var extra []string
	for _, s := range services {
		byDir[strings.ToLower(filepath.Clean(s.Dir))] = s
		extra = append(extra, s.Dir)
	}
	dirs, err := runnerfs.Discover(m.Roots, extra)
	if err != nil {
		errs = append(errs, err)
	}
	runners := make([]domain.Runner, 0, len(dirs))
	for _, dir := range dirs {
		r, err := runnerfs.Read(dir)
		if err != nil {
			r.Problem = err.Error()
		}
		if s, ok := byDir[strings.ToLower(dir)]; ok {
			r.Service, r.State, r.PID = s.Name, s.State, s.PID
		}
		runners = append(runners, r)
	}
	sort.SliceStable(runners, func(a, b int) bool {
		if runners[a].Repo != runners[b].Repo {
			return runners[a].Repo < runners[b].Repo
		}
		return runners[a].Name < runners[b].Name
	})
	return runners, errors.Join(errs...)
}

// Control starts or stops a runner's service.
func (m *Machine) Control(ctx context.Context, service string, start bool) error {
	if m.ControlFn == nil {
		return errors.New("service control is not available")
	}
	return m.ControlFn(ctx, service, start)
}
