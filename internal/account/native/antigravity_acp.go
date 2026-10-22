package native

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"time"
)

const agyUserInfoURL = "https://www.googleapis.com/oauth2/v2/userinfo"

// Refresh contract for the ACP grant. acp_token.json holds client_id,
// client_secret and a long-lived refresh_token, and no access token. The ACP
// server exchanges the refresh token with Google on every launch and rewrites
// the file itself if Google rotates it. Dyna calls Google for this grant only
// at sign-in, to confirm its account. It saves the file verbatim at capture,
// restores it byte for byte on activation, and re-captures it after each use so
// a rotated token replaces the saved copy.

// AntigravityACPTokenFile is relative to the native HOME. The ACP server runs
// with GEMINI_HOME set to that HOME and keeps its own OAuth grant here. This
// grant uses a different OAuth client than the agy CLI token, so one cannot
// stand in for the other.
const AntigravityACPTokenFile = "antigravity-acp/acp_token.json"

// AntigravityACPSettingsFile selects the ACP sign-in method for the server.
const AntigravityACPSettingsFile = "antigravity-acp/settings.json"

// agyACPRole names the optional second entry of a saved Antigravity payload.
const agyACPRole = "file:" + AntigravityACPTokenFile
const agyMaxACPToken = 64 << 10

// agyPayload splits a legacy saved payload into the CLI wrapper and the optional ACP
// token file. Any other entry is a schema error.
func agyPayload(payload []byte) (cli, acp json.RawMessage, err error) {
	var p map[string]json.RawMessage
	if len(payload) > agyMaxAuth || json.Unmarshal(payload, &p) != nil || len(p) == 0 || len(p) > 2 {
		return nil, nil, ErrSchema
	}
	cli = p[agyRole]
	acp = p[agyACPRole]
	if len(cli) == 0 || len(p) == 2 && len(acp) == 0 {
		return nil, nil, ErrSchema
	}
	if len(acp) > 0 {
		if err = agyACPValidate(acp); err != nil {
			return nil, nil, err
		}
	}
	return cli, acp, nil
}

// agyPack joins the CLI wrapper and the optional ACP token file into one payload.
func agyPack(cli, acp json.RawMessage) json.RawMessage {
	p := map[string]json.RawMessage{agyRole: cli}
	if len(acp) > 0 {
		p[agyACPRole] = acp
	}
	raw, _ := json.Marshal(p)
	return raw
}

// agyACPValidate checks the fields the ACP server needs without exposing them.
func agyACPValidate(raw []byte) error {
	var token struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		RefreshToken string `json:"refresh_token"`
	}
	if len(raw) > agyMaxACPToken || json.Unmarshal(raw, &token) != nil || token.ClientID == "" || token.ClientSecret == "" || token.RefreshToken == "" {
		return ErrSchema
	}
	return nil
}

// remove deletes one sign-in file, so a sign-in or a switch never keeps
// another account's grant.
func (s agyFileStore) remove() error {
	info, err := os.Lstat(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("Antigravity credentials could not be removed safely.")
	}
	return os.Remove(s.path)
}

// agyACPIdentity names the Google account an ACP grant signs in as, in the
// "google:<sub>" form, with its email as the label. It exchanges the refresh
// token with the grant's own OAuth client and asks Google who the access token
// belongs to. Only sign-in calls it.
func agyACPIdentity(ctx context.Context, client claudeHTTPClient, raw []byte, now time.Time) (agyACPAccount, error) {
	var token struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		RefreshToken string `json:"refresh_token"`
	}
	if agyACPValidate(raw) != nil || json.Unmarshal(raw, &token) != nil {
		return agyACPAccount{}, ErrSchema
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {token.RefreshToken}, "client_id": {token.ClientID}, "client_secret": {token.ClientSecret}}
	body, err := directRequest(ctx, client, http.MethodPost, agyTokenURL, form.Encode(), map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, now, http.StatusBadRequest)
	if err != nil {
		return agyACPAccount{}, err
	}
	var access struct {
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal(body, &access) != nil || access.AccessToken == "" {
		return agyACPAccount{}, ErrSchema
	}
	body, err = directRequest(ctx, client, http.MethodGet, agyUserInfoURL, "", map[string]string{"Authorization": "Bearer " + access.AccessToken}, now)
	if err != nil {
		return agyACPAccount{}, err
	}
	var info struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	}
	if json.Unmarshal(body, &info) != nil {
		return agyACPAccount{}, ErrNoIdentity
	}
	a := agyACPAccount{Identity: "google:" + info.ID, Label: info.Email}
	if !a.valid() {
		return agyACPAccount{}, ErrNoIdentity
	}
	return a, nil
}
