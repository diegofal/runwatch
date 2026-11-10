package notify

import (
	"fmt"
	"sync"

	toastlib "git.sr.ht/~jackmordaunt/go-toast/v2"
)

var (
	registerOnce sync.Once
	errRegister  error
)

// toast posts a Windows toast through the WinRT API. Windows shows toasts
// only for an app it knows, so the first one registers AppID under
// HKCU\Software\Classes\AppUserModelId (no admin needed). A toast with a
// URL opens it in the default browser when clicked.
func toast(title, body, url string) error {
	registerOnce.Do(func() { errRegister = toastlib.SetAppData(toastlib.AppData{AppID: AppID}) })
	if errRegister != nil {
		return fmt.Errorf("notify: register with Windows: %w", errRegister)
	}
	n := toastlib.Notification{AppID: AppID, Title: title, Body: body}
	if url != "" {
		n.ActivationType = toastlib.Protocol
		n.ActivationArguments = url
		n.Actions = []toastlib.Action{{Type: toastlib.Protocol, Content: "Open", Arguments: url}}
	}
	if err := n.Push(); err != nil {
		return fmt.Errorf("notify %q: %w", title, err)
	}
	return nil
}
