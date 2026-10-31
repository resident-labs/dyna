package model

import "strings"

// CompletionProviders returns only providers whose completions executor passed
// the proxy gate. Other wire formats are enabled by their own gates.
func (c Catalog) CompletionProviders() []CatalogProvider {
	var out []CatalogProvider
	for _, p := range c.Providers {
		if p.KeyName == "" || p.ID == "github-copilot" {
			continue
		}
		var models []CatalogModel
		var base string
		valid := true
		for _, m := range p.Models {
			if m.API != "openai-completions" {
				continue
			}
			if !fixedCatalogURL(m.BaseURL) || (base != "" && base != m.BaseURL) {
				valid = false
				break
			}
			base = m.BaseURL
			models = append(models, m)
		}
		if !valid || len(models) == 0 {
			continue
		}
		headers := make(map[string]string)
		for name, value := range p.Headers {
			lower := strings.ToLower(name)
			if strings.Contains(lower, "auth") || strings.Contains(lower, "key") ||
				strings.Contains(lower, "token") || strings.Contains(lower, "cookie") ||
				lower == "host" || len(value) > 256 {
				continue
			}
			printable := true
			for _, c := range value {
				if c < 32 || c > 126 {
					printable = false
					break
				}
			}
			if printable {
				headers[name] = value
			}
		}
		if len(headers) > 8 {
			continue
		}
		p.Models, p.Headers = models, headers
		out = append(out, p)
	}
	return out
}
