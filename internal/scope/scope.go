// Package scope names the authorization scopes a remote device can hold.
package scope

import (
	"fmt"
	"strings"
)

const (
	Read      = "read"
	Operate   = "operate"
	Files     = "files"
	Terminal  = "terminal"
	Computers = "computers"
	Admin     = "admin"
)

// LegacyFull is how a pre-scope device is shown to the owner.
const LegacyFull = "legacy: full access"

var order = []string{Read, Operate, Files, Terminal, Computers, Admin}

// Default is the grant for a new pairing or Connect session when the owner
// names no scopes.
func Default() []string { return []string{Read, Operate} }

// All is every scope. The local desktop and the local computer browser get it.
func All() []string { return append([]string(nil), order...) }

// Normalize returns the scopes in canonical order, without duplicates.
// An unknown name or an empty list is an error.
func Normalize(in []string) ([]string, error) {
	if len(in) == 0 {
		return nil, fmt.Errorf("name at least one scope: %s", strings.Join(order, ", "))
	}
	have := map[string]bool{}
	for _, raw := range in {
		name := strings.ToLower(strings.TrimSpace(raw))
		if !valid(name) {
			return nil, fmt.Errorf("unknown scope %q (known scopes: %s)", raw, strings.Join(order, ", "))
		}
		have[name] = true
	}
	out := make([]string, 0, len(have))
	for _, name := range order {
		if have[name] {
			out = append(out, name)
		}
	}
	return out, nil
}

func valid(name string) bool {
	for _, known := range order {
		if name == known {
			return true
		}
	}
	return false
}

// Label is the text shown for a device. A legacy grant keeps its own words
// even when it holds every scope, so the owner can tell it from an explicit grant.
func Label(scopes []string, legacy bool) string {
	if legacy {
		return LegacyFull
	}
	if len(scopes) == 0 {
		return "no access"
	}
	return strings.Join(scopes, ", ")
}
