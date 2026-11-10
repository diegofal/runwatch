// Package notify shows desktop notifications: a Windows toast whose click
// opens the dashboard at the job or queue it is about. On other systems a
// notification is only an error saying so; the core logs it.
package notify

import (
	"sync"

	"runwatch/internal/domain"
)

// AppID is the name Windows shows on Runwatch's toasts.
const AppID = "Runwatch"

// Desktop is core.Notifier.
type Desktop struct {
	mu   sync.Mutex
	base string // the dashboard's URL; a toast's click opens base + link
}

// New returns the notifier; base is the dashboard URL ("" until known).
func New(base string) *Desktop { return &Desktop{base: base} }

// SetBase sets the dashboard URL once the server is listening.
func (d *Desktop) SetBase(base string) {
	d.mu.Lock()
	d.base = base
	d.mu.Unlock()
}

// Notify shows an alert. Clicking it opens the dashboard at a.Link.
func (d *Desktop) Notify(a domain.Alert) error {
	return d.Show(a.Title, a.Body, a.Link)
}

// Show shows title and body; link is the dashboard fragment to open on click.
func (d *Desktop) Show(title, body, link string) error {
	d.mu.Lock()
	base := d.base
	d.mu.Unlock()
	url := ""
	if base != "" {
		url = base + "/" + link
	}
	return toast(title, body, url)
}
