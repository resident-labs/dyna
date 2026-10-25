package runtime

// harnessDistributions is the version and install source for each managed CLI.
// npm verifies dist.integrity during install. Vendor installers verify the
// checksums in their release manifests before publishing the bundle.
var harnessDistributions = map[string]struct {
	Kind    string
	Package string
}{
	"claude-code": {"npm", "@anthropic-ai/claude-code"},
	"grok":        {"npm", "@xai-official/grok"},
	"pi":          {"npm", piNPMPackage},
	"kimi-code":   {"npm", "@moonshot-ai/kimi-code"},
}

// npmDistributionMetadata is the registry receipt used for lookup and install.
type npmDistributionMetadata struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Dist    struct {
		Integrity string `json:"integrity"`
	} `json:"dist"`
}
