package websvc

import (
	"path/filepath"
	"strings"

	"dyna-agent/internal/devicegrant"
	"dyna-agent/internal/scope"
)

// ensureComputerBrowser records full access for the local computer browser
// the first time it calls the engine. The computer daemon imports devicestore
// and must not import the grant database. An existing grant stays, so the
// owner can narrow that browser later.
func (s *Server) ensureComputerBrowser(id string) error {
	if !strings.HasPrefix(id, "browser-") {
		return nil
	}
	grants, err := devicegrant.Open(filepath.Dir(s.o.Store.Dir()))
	if err != nil {
		return err
	}
	if _, ok, err := grants.Get("paired", id); err != nil || ok {
		return err
	}
	return grants.Put("paired", id, scope.All(), false)
}
