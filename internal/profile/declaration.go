package profile

import "strings"

// DeclareOutput persists the native default while keeping explicit declarations.
func (p *Profile) DeclareOutput() {
	if p.Output == nil || strings.TrimSpace(p.Output.Dialect) == "" {
		p.Output = p.EffectiveOutput()
	}
}
