package podman

import (
	"errors"
	"testing"

	"github.com/resident-labs/dyna/box"
)

func TestImageFailure(t *testing.T) {
	for _, c := range []struct {
		name, stderr string
		image        bool
	}{
		{"pull", "Error: creating container storage: Trying to pull docker.io/library/x:1...", true},
		{"registry", "Error: initializing source docker://registry.example.com/x:1: pinging container registry registry.example.com: connection refused", true},
		{"short name", "Error: short-name resolution enforced but cannot prompt without a TTY", true},
		{"name unknown", "Error: initializing source docker://x:1: reading manifest 1 in docker.io/library/x: name unknown: repository not found", true},
		{"unauthorized", "Error: unauthorized: authentication required", true},
		{"auth token", "Error: fetching auth token: invalid status code from registry 403", true},
		{"manifest", "Error: manifest unknown", true},
		{"pull denied", "Error: pull access denied, repository does not exist or may require authorization", true},
		{"image not known", "Error: localhost/dyna-base:a: image not known", true},
		{"bad reference", "Error: invalid reference format", true},
		{"cgroup", "Error: creating cgroup: cgroup.subtree_control: permission denied", false},
		{"disk", "Error: writing blob: no space left on device", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := errors.New(c.stderr)
			var imageErr box.ImageCreateError
			if got := errors.As(imageFailure(err), &imageErr); got != c.image {
				t.Fatalf("image failure %v, want %v", got, c.image)
			}
		})
	}
	if imageFailure(nil) != nil {
		t.Fatal("nil became an error")
	}
}
