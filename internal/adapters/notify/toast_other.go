//go:build !windows

package notify

import "errors"

func toast(string, string, string) error {
	return errors.New("notify: desktop notifications are only implemented on Windows")
}
