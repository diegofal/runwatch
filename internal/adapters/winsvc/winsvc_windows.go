// Package winsvc finds the GitHub Actions runner services installed on this
// machine and reads their state without admin rights. Starting or stopping
// one does need admin: Control hands that to Windows, which asks the user
// (UAC).
package winsvc

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"runwatch/internal/adapters/execx"
	"runwatch/internal/domain"
)

const (
	servicesKey = `SYSTEM\CurrentControlSet\Services`
	prefix      = "actions.runner."
)

// Service is one runner service.
type Service struct {
	Name  string
	Dir   string // the runner's install folder
	State domain.ServiceState
}

// List returns the runner services ("actions.runner.*") with their state.
func List() ([]Service, error) {
	root, err := registry.OpenKey(registry.LOCAL_MACHINE, servicesKey, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	defer root.Close() //nolint:errcheck // read-only handle
	names, err := root.ReadSubKeyNames(-1)
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return nil, fmt.Errorf("open the service manager: %w", err)
	}
	defer windows.CloseServiceHandle(scm) //nolint:errcheck // read-only handle
	var out []Service
	var errs []error
	for _, name := range names {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		svc := Service{Name: name, State: domain.ServiceUnknown}
		if svc.Dir, err = installDir(name); err != nil {
			errs = append(errs, err)
			continue
		}
		if svc.State, err = state(scm, name); err != nil {
			errs = append(errs, err)
		}
		out = append(out, svc)
	}
	return out, errors.Join(errs...)
}

// installDir is the folder above the bin folder the service's program is in.
func installDir(name string) (string, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, servicesKey+`\`+name, registry.QUERY_VALUE)
	if err != nil {
		return "", fmt.Errorf("service %s: %w", name, err)
	}
	defer k.Close() //nolint:errcheck // read-only handle
	image, _, err := k.GetStringValue("ImagePath")
	if err != nil {
		return "", fmt.Errorf("service %s: read ImagePath: %w", name, err)
	}
	image = strings.TrimSpace(image)
	if strings.HasPrefix(image, `"`) {
		if end := strings.Index(image[1:], `"`); end >= 0 {
			image = image[1 : end+1]
		}
	}
	return filepath.Dir(filepath.Dir(image)), nil
}

func state(scm windows.Handle, name string) (domain.ServiceState, error) {
	ptr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return domain.ServiceUnknown, fmt.Errorf("service %s: %w", name, err)
	}
	h, err := windows.OpenService(scm, ptr, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return domain.ServiceUnknown, fmt.Errorf("service %s: %w", name, err)
	}
	defer windows.CloseServiceHandle(h) //nolint:errcheck // read-only handle
	var st windows.SERVICE_STATUS
	if err := windows.QueryServiceStatus(h, &st); err != nil {
		return domain.ServiceUnknown, fmt.Errorf("service %s: %w", name, err)
	}
	if st.CurrentState == windows.SERVICE_STOPPED || st.CurrentState == windows.SERVICE_STOP_PENDING {
		return domain.ServiceStopped, nil
	}
	return domain.ServiceRunning, nil
}

// Control starts or stops a runner service. Windows shows its admin prompt;
// declining it is an error.
func Control(ctx context.Context, name string, start bool) error {
	if !strings.HasPrefix(name, prefix) || strings.ContainsAny(name, `'"`+"`$;& \t\r\n") {
		return fmt.Errorf("refusing to control %q: not a runner service", name)
	}
	verb := "stop"
	if start {
		verb = "start"
	}
	// sc.exe runs elevated and -Wait passes its exit code back.
	script := fmt.Sprintf(`$p = Start-Process sc.exe -ArgumentList '%s','%s' -Verb RunAs -WindowStyle Hidden -Wait -PassThru; exit $p.ExitCode`, verb, name)
	if _, err := execx.Run(ctx, "", nil, "powershell", "-NoProfile", "-NonInteractive", "-Command", script); err != nil {
		return fmt.Errorf("%s service %s (needs admin): %w", verb, name, err)
	}
	return nil
}
