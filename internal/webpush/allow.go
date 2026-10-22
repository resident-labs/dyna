package webpush

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
)

// ValidateEndpoint accepts only known push services over https. It exists so
// the machine cannot be made to post to any other URL, its own LAN included.
func ValidateEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return errors.New("webpush: endpoint is not a valid URL")
	}
	if u.Scheme != "https" {
		return errors.New("webpush: endpoint must use https")
	}
	if u.User != nil {
		return errors.New("webpush: endpoint must not have user info")
	}
	if p := u.Port(); p != "" && p != "443" {
		return errors.New("webpush: endpoint must use the default port")
	}
	host := strings.ToLower(u.Hostname())
	if _, err := netip.ParseAddr(host); err == nil {
		return errors.New("webpush: endpoint host must be a name, not an IP address")
	}
	if !allowedHost(host) {
		return fmt.Errorf("webpush: %q is not a known push service", host)
	}
	return nil
}

// jmtHost is the push host Chrome hands out since 2026, for example
// jmt17.google.com. Only that shape passes, not any google.com name.
var jmtHost = regexp.MustCompile(`^jmt[0-9]{1,3}\.google\.com$`)

func allowedHost(h string) bool {
	switch h {
	case "fcm.googleapis.com", "web.push.apple.com":
		return true
	}
	if jmtHost.MatchString(h) {
		return true
	}
	for _, suf := range []string{".push.services.mozilla.com", ".notify.windows.com"} {
		if strings.HasSuffix(h, suf) && len(h) > len(suf) {
			return true
		}
	}
	return false
}
