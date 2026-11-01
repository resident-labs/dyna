// Package images supplies the base image build inputs.
package images

import "embed"

// BaseContainerfile is the dyna-base image recipe.
//
//go:embed dyna-base/Containerfile
var BaseContainerfile []byte

// Runner holds the CI runner image context.
//
//go:embed ci-runner
var Runner embed.FS
