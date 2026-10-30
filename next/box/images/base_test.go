package images

import (
	"strings"
	"testing"
)

func TestBaseImageProvidesWritableRepositoryDirectory(t *testing.T) {
	recipe := string(BaseContainerfile)
	if !strings.Contains(recipe, "mkdir -p /box/home /box/pi /box/work") ||
		!strings.Contains(recipe, "chown node:node /box /box/home /box/pi /box/work") {
		t.Fatal("the base image must make /box and /box/work writable by the node user")
	}
}
