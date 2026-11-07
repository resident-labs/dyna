package podman

import (
	"context"
	"fmt"
	"github.com/resident-labs/dyna/box"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewTypedStreams(t *testing.T) {
	for _, tc := range []struct {
		name, out, diag string
		code            int
		recover         bool
	}{
		{"stderrOnly", "", "Error: no such container \"b\"\n", 125, true},
		{"pairedObserved", "[]\n", "Error: no such container \"b\"\n", 125, true},
		{"pairedNearID", "[]\n", "Error: no such container \"b-other\"\n", 125, false},
		{"pairedExtraLine", "[]\n", "Error: no such container \"b\"\nI/O error\n", 125, false},
		{"pairedBlankLine", "[]\n", "Error: no such container \"b\"\n\n", 125, false},
		{"pairedUnknownStatus", "[]\n", "Error: no such container \"b\"\n", 255, false},
		{"pairedStorageID", "[]\n", "Error: getting container from store \"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\": container not known\n", 125, false},
	} {
		for _, mode := range []string{"create", "recreate", "warm"} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				root, h := namespaceFixture(t)
				tools := t.TempDir()
				t.Setenv("PATH", tools+":"+os.Getenv("PATH"))
				t.Setenv("REVIEW_OUT", tc.out)
				t.Setenv("REVIEW_DIAG", tc.diag)
				t.Setenv("REVIEW_STATUS", fmt.Sprint(tc.code))
				script := `#!/bin/bash
[ "$1" != --cgroup-manager=systemd ] || shift
printf '%s\n' "$*" >> "$NS_ROOT/review-calls"
case "$1" in
 create|start|stop) exit 125;;
 ps) echo 'b|exited|aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa';;
 inspect) printf '%s' "$REVIEW_OUT"; printf '%s' "$REVIEW_DIAG" >&2; exit "$REVIEW_STATUS";;
 rm) [ "$*" = 'rm -f b' ] || exit 91;;
 pod) exit 0;;
esac
`
				if err := os.WriteFile(filepath.Join(tools, "podman"), []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
				switch mode {
				case "create":
					_ = h.CreateContainer(context.Background(), box.ContainerSpec{Box: "b", Image: "fake"})
				case "recreate":
					_ = h.CreateBox(context.Background(), box.ContainerSpec{Box: "b", Image: "fake"}, nil, true)
				case "warm":
					_ = h.StartContainer(context.Background(), "b")
				}
				b, _ := os.ReadFile(filepath.Join(root, "review-calls"))
				if strings.Contains(string(b), "rm -f b\n") != tc.recover {
					t.Fatalf("recover want %v: %s", tc.recover, b)
				}
			})
		}
	}
}
