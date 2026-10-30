package bundled

import "embed"

// FS contains the workflows shipped with Dyna.
//
//go:embed *.js
var FS embed.FS
