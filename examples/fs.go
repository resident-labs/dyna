// Package examples embeds the maintained starter workflow files shipped with
// dyna-agent. The .js files in this directory remain the hand-maintained
// source; the embed is only a packaging seam for install and update seeding.
package examples

import "embed"

// FS holds every shipped starter in this directory.
//
//go:embed *.js
var FS embed.FS
