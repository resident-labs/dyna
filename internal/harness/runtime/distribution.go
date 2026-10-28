package runtime

import "os"

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
	piForkVersion      = "1.4.0-dyna.1"
	minimumPiForkLevel = 1
	piForkTarballEnv   = "DYNA_PI_TARBALL"
)

// The release workflow stamps these variables with -X ldflags next to
// main.version. Source builds stamp a local path and integrity.
var (
	piForkURL       string
	piForkIntegrity string
	piForkAsset     string
)

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
