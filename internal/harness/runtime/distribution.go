package runtime

import (
	"os"
	"strings"
)

// distributionKind identifies a managed CLI's install source.
type distributionKind uint8

const (
	distributionNPM distributionKind = iota
	distributionFork
)

type harnessDistribution struct {
	Kind    distributionKind
	Package string
}

// harnessDistributions is the version and install source for each managed CLI.
// npm verifies dist.integrity during install. Vendor installers verify the
// checksums in their release manifests before publishing the bundle.
var harnessDistributions = map[string]harnessDistribution{
	"claude-code": {distributionNPM, "@anthropic-ai/claude-code"},
	"grok":        {distributionNPM, "@xai-official/grok"},
	"pi":          {distributionFork, piNPMPackage},
	"kimi-code":   {distributionNPM, "@moonshot-ai/kimi-code"},
}

const (
	piForkVersion      = "1.4.0-dyna.9"
	minimumPiForkLevel = 9
	piForkTarballEnv   = "DYNA_PI_TARBALL"
)

// The release workflow stamps these variables with -X ldflags next to
// main.version. Source builds stamp a local path and integrity.
var (
	piForkURL       string
	piForkIntegrity string
	piForkAsset     string
	piForkBuild     string
)

// piForkInstallVersion names the exact fork build this Dyna installs: the
// level-only version, or with its Pi tree hash when the release or a source
// build stamped one. An unstamped binary falls back to the level-only
// version and reads the build from a local tarball at install time.
func piForkInstallVersion() string {
	if !isForkBuildHash(piForkBuild) {
		return piForkVersion
	}
	return piForkVersion + "." + piForkBuild
}

// isForkBuildVersion reports whether v names one fork build: the level-only
// version extended by the twelve hex characters of its Pi tree hash.
func isForkBuildVersion(v string) bool {
	build, ok := strings.CutPrefix(v, piForkVersion+".")
	return ok && isForkBuildHash(build)
}

// isForkBuildHash reports whether build is the twelve lowercase hex
// characters pi-pack.sh writes from the third_party/pi tree hash.
func isForkBuildHash(build string) bool {
	if len(build) != 12 {
		return false
	}
	for _, c := range build {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func piForkSource() string {
	if path := os.Getenv(piForkTarballEnv); path != "" {
		return path
	}
	if piForkURL != "" {
		return piForkURL
	}
	if piForkAsset != "" {
		return piForkAsset
	}
	return "development Pi fork (" + piForkTarballEnv + " is not set)"
}

// npmDistributionMetadata is the registry receipt used for lookup and install.
type npmDistributionMetadata struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Dist    struct {
		Integrity string `json:"integrity"`
	} `json:"dist"`
}
