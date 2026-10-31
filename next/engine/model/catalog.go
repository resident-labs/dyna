package model

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"regexp"
	"strings"
)

const catalogLimit = 2 << 20

// Catalog is the validated, pinned Pi provider catalog.
type Catalog struct {
	Version   int               `json:"version"`
	Providers []CatalogProvider `json:"providers"`
}

type CatalogProvider struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	KeyName string            `json:"keyName"`
	Headers map[string]string `json:"headers"`
	Models  []CatalogModel    `json:"models"`
}

type CatalogModel struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	API           string `json:"api"`
	BaseURL       string `json:"baseUrl"`
	Reasoning     bool   `json:"reasoning"`
	ContextWindow int    `json:"contextWindow"`
	MaxTokens     int    `json:"maxTokens"`
}

var catalogProviderID = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,63}$`)

// CatalogError reports a missing or invalid packed Pi catalog.
type CatalogError struct{ Cause error }

func (e *CatalogError) Error() string {
	return "invalid Pi catalog; rebuild pi.tgz with pi-pack.sh: " + e.Cause.Error()
}
func (e *CatalogError) Unwrap() error { return e.Cause }

// LoadCatalog reads the catalog from the packed Pi artifact. Invalid providers
// are returned separately so the caller can log their IDs.
func LoadCatalog(pi []byte) (Catalog, []string, error) {
	fail := func(err error) (Catalog, []string, error) {
		return Catalog{}, nil, &CatalogError{Cause: err}
	}
	gz, err := gzip.NewReader(bytes.NewReader(pi))
	if err != nil {
		return fail(err)
	}
	defer gz.Close()
	tarball := tar.NewReader(gz)
	for {
		header, err := tarball.Next()
		if errors.Is(err, io.EOF) {
			return fail(errors.New("catalog is missing"))
		}
		if err != nil {
			return fail(err)
		}
		if header.Name != "package/dyna-catalog.json" {
			continue
		}
		if header.Size < 0 || header.Size > catalogLimit {
			return fail(errors.New("catalog exceeds size limit"))
		}
		data, err := io.ReadAll(io.LimitReader(tarball, catalogLimit+1))
		if err != nil || len(data) > catalogLimit {
			return fail(errors.New("cannot read catalog"))
		}
		var catalog Catalog
		if err := json.Unmarshal(data, &catalog); err != nil {
			return fail(err)
		}
		if catalog.Version != 1 || len(catalog.Providers) == 0 || len(catalog.Providers) > 200 {
			return fail(errors.New("unsupported catalog version or provider count"))
		}
		valid := make([]CatalogProvider, 0, len(catalog.Providers))
		var skipped []string
		for _, provider := range catalog.Providers {
			if reason := invalidCatalogProvider(provider); reason != "" {
				skipped = append(skipped, provider.ID+": "+reason)
				continue
			}
			valid = append(valid, provider)
		}
		catalog.Providers = valid
		return catalog, skipped, nil
	}
}

func invalidCatalogProvider(p CatalogProvider) string {
	if !catalogProviderID.MatchString(p.ID) || len(p.Models) > 500 {
		return "invalid provider ID or model count"
	}
	for _, value := range p.Headers {
		if strings.HasPrefix(value, "$") {
			return "a header value cannot start with $"
		}
	}
	for _, m := range p.Models {
		if len(m.ID) == 0 || len(m.ID) > 200 {
			return "invalid model ID"
		}
		for _, c := range m.ID {
			if c < 33 || c > 126 {
				return "invalid model ID"
			}
		}
	}
	return ""
}

// fixedCatalogURL excludes dynamic destinations and non-default HTTPS ports.
func fixedCatalogURL(raw string) bool {
	if strings.ContainsAny(raw, "{}$#") {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil &&
		u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && (u.Port() == "" || u.Port() == "443")
}
