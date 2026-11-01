package model

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"github.com/resident-labs/dyna/contract"
	"io"
)

const catalogLimit = 2 << 20

// Catalog is the validated, pinned Pi provider catalog.
type Catalog struct {
	Version int            `json:"version"`
	Plans   []PlanProvider `json:"plans"`
	Keys    []KeyProvider  `json:"keys"`
}

type PlanProvider struct {
	ID           contract.AccountProvider `json:"id"`
	DefaultModel string                   `json:"defaultModel"`
	Models       []PlanModel              `json:"models"`
}

type PlanModel struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	ProxyID string `json:"proxyID"`
}

type KeyProvider struct {
	Provider     contract.AccountProvider `json:"provider"`
	DisplayName  string                   `json:"displayName"`
	KeyName      string                   `json:"keyName"`
	DefaultModel string                   `json:"defaultModel"`
	URL          string                   `json:"url"`
	Headers      map[string]string        `json:"headers"`
	Models       []contract.CatalogModel  `json:"models"`
}

// CatalogError reports a missing or invalid packed Pi catalog.
type CatalogError struct{ Cause error }

func (e *CatalogError) Error() string {
	return "invalid Pi catalog; rebuild pi.tgz with pi-pack.sh: " + e.Cause.Error()
}
func (e *CatalogError) Unwrap() error { return e.Cause }

// LoadCatalog reads the catalog from the packed Pi artifact.
func LoadCatalog(pi []byte) (Catalog, error) {
	fail := func(err error) (Catalog, error) {
		return Catalog{}, &CatalogError{Cause: err}
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
		if catalog.Version != 1 {
			return fail(errors.New("unsupported catalog version"))
		}
		return catalog, nil
	}
}
