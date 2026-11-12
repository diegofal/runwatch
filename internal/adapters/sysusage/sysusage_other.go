//go:build !windows

// Package sysusage reads how busy this PC is. Only Windows is supported
// today; elsewhere the read says so, and Runwatch shows it as a problem.
package sysusage

import (
	"context"
	"errors"

	"runwatch/internal/domain"
)

// Read is not available on this OS.
func Read(context.Context) (domain.Reading, error) {
	return domain.Reading{}, errors.New("CPU and memory are only measured on Windows")
}
