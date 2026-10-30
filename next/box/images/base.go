// Package images supplies the base image build inputs.
package images

import _ "embed"

// BaseContainerfile is the dyna-base image recipe.
//go:embed dyna-base/Containerfile
var BaseContainerfile []byte
