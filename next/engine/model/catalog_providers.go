package model

import "github.com/resident-labs/dyna/contract"

// KeyProvider describes a fixed upstream and its permitted models.
type KeyProvider struct {
	Provider     contract.AccountProvider
	DisplayName  string
	KeyName      string
	DefaultModel string
	URL          string
	Headers      map[string]string
	Models       []contract.CatalogModel
}

// KeyProviders returns completions providers admitted by the Pi catalog.
func (c Catalog) KeyProviders() []KeyProvider {
	var out []KeyProvider
	for _, p := range c.CompletionProviders() {
		entry := KeyProvider{Provider: contract.AccountProvider(p.ID), DisplayName: p.Name, KeyName: p.KeyName, URL: p.Models[0].BaseURL, Headers: p.Headers, DefaultModel: p.Models[0].ID}
		for _, m := range p.Models {
			entry.Models = append(entry.Models, contract.CatalogModel{ID: m.ID, Name: m.Name})
			if m.ID == p.DefaultModel {
				entry.DefaultModel = m.ID
			}
		}
		out = append(out, entry)
	}
	return out
}
