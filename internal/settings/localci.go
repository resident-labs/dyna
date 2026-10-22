package settings

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

const KeyLocalCIRemote = "localci.remote"
const DefaultLocalCIRemote = "ci@build-host:/var/tmp/dyna-local-ci"

// ParseLocalCIRemote keeps SSH destinations and cache paths free of shell syntax.
func ParseLocalCIRemote(value string) (host, cache string, err error) {
	host, cache, ok := strings.Cut(value, ":")
	if !ok || !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.@-]*$`).MatchString(host) || !regexp.MustCompile(`^/[a-zA-Z0-9_./-]+$`).MatchString(cache) || path.Clean(cache) == "/" || path.Clean(cache) != cache {
		return "", "", fmt.Errorf("localci.remote must be user@host:/absolute/cache/path")
	}
	return host, cache, nil
}
