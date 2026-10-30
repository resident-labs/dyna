// Package devicesvc manages paired devices and connect sessions.
package devicesvc

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"dyna-agent/internal/connect"
	"dyna-agent/internal/devicegrant"
	"dyna-agent/internal/scope"
	"dyna-agent/internal/websvc"
	"dyna-agent/internal/websvc/devicestore"
)

// Service reads device stores beside the supplied session store root.
type Service struct {
	sessionRoot string
}

// New uses the same data directory as the session store.
func New(sessionRoot string) *Service {
	return &Service{sessionRoot: sessionRoot}
}

// DeviceList marks the calling browser only when web dispatch supplies its
// authenticated principal. The desktop receives the unchanged list.
type DeviceList []Device

func (list DeviceList) ForPrincipal(p websvc.Principal) any {
	out := append(DeviceList{}, list...)
	kind := p.Kind
	if kind == "device" {
		kind = "paired"
	}
	for i := range out {
		out[i].Current = p.ID != "" && out[i].Kind == kind && out[i].ID == p.ID
	}
	return out
}

func (s *Service) deviceStores() (*devicestore.Store, *connect.Store, error) {
	root := filepath.Dir(s.sessionRoot)
	w, err := devicestore.OpenStore(filepath.Join(root, "web"))
	if err != nil {
		return nil, nil, err
	}
	c, err := connect.OpenStore(filepath.Join(root, "connect"))
	return w, c, err
}
func (s *Service) Devices() (DeviceList, error) {
	w, c, err := s.deviceStores()
	if err != nil {
		return nil, err
	}
	paired, err := w.Devices()
	if err != nil {
		return nil, err
	}
	sessions, err := c.Sessions()
	if err != nil {
		return nil, err
	}
	grants, err := devicegrant.Open(filepath.Dir(s.sessionRoot))
	if err != nil {
		return nil, err
	}
	pairedGrants, err := grants.Map("paired")
	if err != nil {
		return nil, err
	}
	connectGrants, err := grants.Map("connect")
	if err != nil {
		return nil, err
	}
	out := DeviceList{}
	for _, d := range paired {
		out = append(out, withGrant(Device{ID: d.ID, Kind: "paired", Name: d.Name, Created: d.Created, LastSeen: d.LastSeen, Expires: d.Expires}, pairedGrants[d.ID]))
	}
	for _, d := range sessions {
		out = append(out, withGrant(Device{ID: d.ID, Kind: "connect", Name: d.Name, Created: d.Created, LastSeen: d.LastSeen, Expires: d.End, Origin: d.Origin, Device: d.Device}, connectGrants[d.ID]))
	}
	return out, nil
}

func withGrant(d Device, g devicegrant.Grant) Device {
	d.Scopes = append([]string(nil), g.Scopes...)
	d.Legacy = g.Legacy
	d.ScopeLabel = scope.Label(g.Scopes, g.Legacy)
	return d
}
func (s *Service) RevokeDevice(kind, id string) (int, error) {
	if kind != "paired" && kind != "connect" && kind != "all" {
		return 0, fmt.Errorf("unknown device kind")
	}
	if id == "" {
		return 0, fmt.Errorf("device ID is required")
	}
	w, c, err := s.deviceStores()
	if err != nil {
		return 0, err
	}
	grants, err := devicegrant.Open(filepath.Dir(s.sessionRoot))
	if err != nil {
		return 0, err
	}
	n := 0
	if kind == "paired" || kind == "all" {
		list, err := w.Devices()
		if err != nil {
			return 0, err
		}
		var removed []string
		for _, d := range list {
			if id == "all" || d.ID == id {
				removed = append(removed, d.ID)
			}
		}
		if len(removed) > 0 {
			n, err = w.Revoke(id)
			if err != nil {
				return n, err
			}
			if err := grants.Delete("paired", removed); err != nil {
				return n, err
			}
		}
	}
	if kind == "connect" || kind == "all" {
		sessions, err := c.Sessions()
		if err != nil {
			return n, err
		}
		var removed []string
		for _, d := range sessions {
			if id == "all" || d.ID == id || d.Device == id {
				removed = append(removed, d.ID)
			}
		}
		m, err := c.RevokeSessions(id)
		n += m
		if err != nil {
			return n, err
		}
		if err := grants.Delete("connect", removed); err != nil {
			return n, err
		}
	}
	return n, nil
}

// SetScopes replaces the grant for one paired device or Connect session.
// The new grant is explicit, so a legacy full-access row no longer applies.
func (s *Service) SetScopes(id string, scopes []string) (Device, error) {
	scopes, err := scope.Normalize(scopes)
	if err != nil {
		return Device{}, err
	}
	if id == "" || id == "all" {
		return Device{}, fmt.Errorf("device ID is required")
	}
	w, c, err := s.deviceStores()
	if err != nil {
		return Device{}, err
	}
	paired, err := w.Devices()
	if err != nil {
		return Device{}, err
	}
	sessions, err := c.Sessions()
	if err != nil {
		return Device{}, err
	}
	var found *Device
	for _, d := range paired {
		if d.ID == id {
			found = &Device{ID: d.ID, Kind: "paired", Name: d.Name, Created: d.Created, LastSeen: d.LastSeen, Expires: d.Expires}
			break
		}
	}
	for _, d := range sessions {
		if d.ID != id {
			continue
		}
		if found != nil {
			return Device{}, fmt.Errorf("device id %s matches both a paired device and a Connect session", id)
		}
		found = &Device{ID: d.ID, Kind: "connect", Name: d.Name, Created: d.Created, LastSeen: d.LastSeen, Expires: d.End, Origin: d.Origin, Device: d.Device}
		break
	}
	if found == nil {
		return Device{}, fmt.Errorf("no paired device or Connect session has id %s", id)
	}
	grants, err := devicegrant.Open(filepath.Dir(s.sessionRoot))
	if err != nil {
		return Device{}, err
	}
	if err := grants.Put(found.Kind, found.ID, scopes, false); err != nil {
		return Device{}, err
	}
	found.Scopes = append([]string(nil), scopes...)
	found.ScopeLabel = scope.Label(scopes, false)
	return *found, nil
}

// appOrigin is the web app's origin: the linked machine's address, else the local listener.
func appOrigin(c *connect.Store) (string, error) {
	link, ok, err := c.Link()
	if err != nil {
		return "", err
	}
	if ok && !link.Unlinked {
		return link.App, nil
	}
	return "http://127.0.0.1:7457", nil
}

// AppOrigin returns the origin people open the web app at.
func (s *Service) AppOrigin() (string, error) {
	_, c, err := s.deviceStores()
	if err != nil {
		return "", err
	}
	return appOrigin(c)
}
func (s *Service) CreatePairing(origin string) (Pairing, error) {
	w, c, err := s.deviceStores()
	if err != nil {
		return Pairing{}, err
	}
	origin = strings.TrimSpace(origin)
	if origin == "" {
		if origin, err = appOrigin(c); err != nil {
			return Pairing{}, err
		}
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return Pairing{}, fmt.Errorf("enter the web app origin, such as https://dyna.example.com")
	}
	code, err := w.NewCode("")
	if err != nil {
		return Pairing{}, err
	}
	u.Path = "/"
	u.Fragment = "pair=" + code
	return Pairing{Code: code, URL: u.String(), Expires: time.Now().Add(devicestore.CodeLife)}, nil
}
