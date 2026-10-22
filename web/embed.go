// Package web provides the embedded frontend assets for the engine.
package web

import "embed"

// Assets contains the built frontend in dist.
//
//go:embed all:dist
var Assets embed.FS
