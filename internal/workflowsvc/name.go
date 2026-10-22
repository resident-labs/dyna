package workflowsvc

import "strings"

// MetaName extracts meta.name from a script via a light textual scan.
func MetaName(src, fallback string) string {
	if i := strings.Index(src, "name:"); i >= 0 {
		rest := src[i+5:]
		if q := strings.IndexAny(rest, "'\""); q >= 0 {
			quote := rest[q]
			rest = rest[q+1:]
			if e := strings.IndexByte(rest, quote); e >= 0 {
				return rest[:e]
			}
		}
	}
	base := fallback
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	return strings.TrimSuffix(base, ".js")
}
