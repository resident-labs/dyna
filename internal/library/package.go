package library

import (
	"path"
	"strings"
)

// PackageOwners returns the ids of live entries that own the package a test
// report names. A report names a directory for Go and usually a file for the
// web suites. A Go package can carry the module prefix, which the map never
// does, so the reported path is tried first and with its first segment
// dropped. The first candidate that any entry owns wins.
func PackageOwners(entries []Entry, pkg string) []string {
	for _, candidate := range packageCandidates(pkg) {
		if owners := Owners(entries, candidate); len(owners) > 0 {
			return owners
		}
	}
	return nil
}

// packageCandidates turns a reported package into repository paths to match.
func packageCandidates(pkg string) []string {
	pkg = strings.Trim(strings.TrimSpace(pkg), "/")
	if pkg == "" {
		return nil
	}
	var out []string
	for _, name := range []string{pkg, withoutFirst(pkg)} {
		if name == "" {
			continue
		}
		file := name
		// A web suite names its file; a Go package names its directory.
		if !strings.Contains(path.Base(name), ".") {
			file = path.Join(name, "package_test.go")
		}
		out = append(out, file)
	}
	return out
}

func withoutFirst(pkg string) string {
	if i := strings.Index(pkg, "/"); i > 0 {
		return pkg[i+1:]
	}
	return ""
}
