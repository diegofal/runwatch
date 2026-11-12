//go:build !windows

// Package winsvc finds runner services; on this OS there are none to find:
// runners are discovered from the configured folders only.
package winsvc

import (
	"context"
	"errors"

	"runwatch/internal/domain"
)

// Service is one runner service.
type Service struct {
	Name  string
	Dir   string
	State domain.ServiceState
	PID   int
}

// List returns no services on this OS.
func List() ([]Service, error) { return nil, nil }

// Control is not available on this OS.
func Control(context.Context, string, bool) error {
	return errors.New("starting and stopping runner services is only supported on Windows")
}
