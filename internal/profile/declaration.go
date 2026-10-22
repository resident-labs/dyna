package profile

import "strings"

// DeclareOutput materializes defaults for the version 2 migration.
// Ordinary saves keep omitted declarations and use runtime defaults.
func (p *Profile) DeclareOutput() {
	if p.Output == nil || strings.TrimSpace(p.Output.Dialect) == "" {
		p.Output = p.EffectiveOutput()
	}
}
