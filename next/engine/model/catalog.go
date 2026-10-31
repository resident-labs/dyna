package model

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
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

// LoadCatalog reads the catalog from the packed Pi artifact. Invalid providers
// are returned separately so the caller can log their IDs.
func LoadCatalog(pi []byte) (Catalog, []string, error) {
	fail := func(err error) (Catalog, []string, error) {
		return Catalog{}, nil, fmt.Errorf("invalid Pi catalog; rebuild pi.tgz with pi-pack.sh: %w", err)
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
			if !validCatalogProvider(provider) {
				skipped = append(skipped, provider.ID)
				continue
			}
			valid = append(valid, provider)
		}
		catalog.Providers = valid
		return catalog, skipped, nil
	}
}

func validCatalogProvider(p CatalogProvider) bool {
	if !catalogProviderID.MatchString(p.ID) || len(p.Models) > 500 {
		return false
	}
	for _, m := range p.Models {
		if len(m.ID) == 0 || len(m.ID) > 200 {
			return false
		}
		for _, c := range m.ID {
			if c < 33 || c > 126 {
				return false
			}
		}
	}
	return true
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
