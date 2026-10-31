package shell

import "github.com/resident-labs/dyna/harness"

// Mask hides secrets in command output.
func Mask(text string) string { return harness.Mask(text) }
