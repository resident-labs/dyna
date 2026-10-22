//go:build linux

package computers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"dyna-agent/internal/store"
	"dyna-agent/internal/websvc/devicestore"
)

// browserDeviceLife bounds a browser's sign-in if no revoke ever runs. Each browser start
// signs in again, so a browser in use keeps a live device.
const browserDeviceLife = 2 * time.Hour

// browserAppURL is the installed app the browser opens after it signs in.
const browserAppURL = "http://127.0.0.1:7457"

func browserDeviceID(id string) string {
	sum := sha256.Sum256([]byte(id))
	return "browser-" + hex.EncodeToString(sum[:4])
}

// signInBrowser issues a short-lived device for this computer's browser and gives its token to
// the browser as a session cookie. It replaces the device of an earlier call. The token goes
// from the pairing store straight into the Chromium command and is never printed or logged.
func signInBrowser(ctx context.Context, id string) error {
	devices, err := devicestore.DefaultStore()
	if err != nil {
		return err
	}
	name := id
	if len(name) > 24 {
		name = name[len(name)-24:]
	}
	token, _, err := devices.IssueDevice(browserDeviceID(id), "dyna browser · "+name, browserDeviceLife)
	if err != nil {
		return err
	}
	return browserCommand(ctx, id, "Storage.setCookies", map[string]any{"cookies": []map[string]any{{
		"name": devicestore.CookieName, "value": token, "domain": "127.0.0.1", "path": "/", "httpOnly": true, "sameSite": "Strict",
	}}})
}

// revokeBrowserDevice removes this computer's browser device if it was issued before cutoff. A zero
// cutoff removes it always. The cutoff keeps a late exit from removing a device a newer browser got.
// It does nothing when there is no device.
func revokeBrowserDevice(id string, cutoff time.Time) {
	dir := filepath.Join(store.DataDir(), "web")
	if _, err := os.Stat(dir); err != nil {
		return
	}
	devices, err := devicestore.OpenStore(dir)
	if err != nil {
		return
	}
	list, err := devices.Devices()
	if err != nil || !slices.ContainsFunc(list, func(d devicestore.Device) bool {
		return d.ID == browserDeviceID(id) && (cutoff.IsZero() || d.Created.Before(cutoff))
	}) {
		return
	}
	_, _ = devices.Revoke(browserDeviceID(id))
}

func logSignInFailure(id string, err error) {
	if f, ferr := os.OpenFile(filepath.Join(Dir(id), "browser.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); ferr == nil {
		fmt.Fprintf(f, "browser sign-in failed: %v\n", err)
		f.Close()
	}
}
