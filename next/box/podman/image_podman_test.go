package podman

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// hereShell runs host commands on this machine instead of over SSH.
type hereShell struct{}

func (hereShell) Run(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	if slices.Contains(args, "-O") || slices.Contains(args, "-M") {
		return nil, nil
	}
	c := exec.CommandContext(ctx, "bash", "-c", args[len(args)-1])
	c.Stdin = bytes.NewReader(input)
	return c.Output()
}

func realPodman(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("podman", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("podman %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func imageExists(name string) bool {
	return exec.Command("podman", "image", "exists", name).Run() == nil
}

// contextFrom returns a build context whose image gets a top layer of its own.
func contextFrom(t *testing.T, content string) []byte {
	t.Helper()
	file := fmt.Sprintf("ARG DYNA_BASE\nFROM ${DYNA_BASE}\nRUN echo %s > /built\n", content)
	var out bytes.Buffer
	w := tar.NewWriter(&out)
	if err := w.WriteHeader(&tar.Header{Name: "Containerfile", Mode: 0644, Size: int64(len(file))}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(file)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// mappedLayers reads Podman's own record of an image's keep-id copies.
func mappedLayers(t *testing.T, image string) []string {
	t.Helper()
	id := realPodman(t, "image", "inspect", "--format", "{{.Id}}", image)
	root := realPodman(t, "info", "--format", "{{.Store.GraphRoot}}")
	data, err := os.ReadFile(filepath.Join(root, "overlay-images", "images.json"))
	if err != nil {
		t.Fatal(err)
	}
	var images []struct {
		ID     string   `json:"id"`
		Mapped []string `json:"mapped-layers"`
	}
	if err := json.Unmarshal(data, &images); err != nil {
		t.Fatal(err)
	}
	for _, record := range images {
		if record.ID == id {
			return record.Mapped
		}
	}
	t.Fatalf("image %s is not in the store", id)
	return nil
}

// On real podman: a warm leaves its mapped copy behind, and prune obeys every rule. DYNA_PODMAN_TEST_BASE names a small
// local image, since keep-id copies the whole image on first use and holds the store lock meanwhile.
func TestRealImagesWarmAndPrune(t *testing.T) {
	base := os.Getenv("DYNA_PODMAN_TEST_BASE")
	if base == "" {
		t.Skip("DYNA_PODMAN_TEST_BASE names a local image; this test runs real podman")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	h, err := New("local", t.TempDir(), "host", hereShell{})
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().UnixNano()
	family := fmt.Sprintf("dyna-img-realtest%d", stamp)
	tags := make([]string, 4)
	for i := range tags {
		tags[i] = fmt.Sprintf("%s:%d", family, i)
		if err := h.Build(ctx, tags[i], base, contextFrom(t, fmt.Sprintf("%d-%d", stamp, i))); err != nil {
			t.Fatal(err)
		}
		// Image times have one-second resolution.
		time.Sleep(1100 * time.Millisecond)
	}
	container := fmt.Sprintf("realtest-%d", stamp)
	defer exec.Command("podman", "rm", "-f", "-t0", container).Run()
	defer func() {
		for _, tag := range tags {
			exec.Command("podman", "rmi", "--force", tag).Run()
		}
	}()

	if layers := mappedLayers(t, tags[3]); len(layers) != 1 {
		t.Fatalf("warmed image has mapped layers %v", layers)
	}
	if out, _ := exec.Command("podman", "ps", "-a", "--format", "{{.Names}}").Output(); strings.Contains(string(out), warmName) {
		t.Fatalf("the dummy container stayed: %s", out)
	}

	// The real create reuses the warmed copy, so the image gains no second one.
	realPodman(t, "create", "--name", container, boxUserNS, "--user", "1000:1000", tags[3], "true")
	if layers := mappedLayers(t, tags[3]); len(layers) != 1 {
		t.Fatalf("a box made its own copy: %v", layers)
	}
	realPodman(t, "rm", container)

	// A stopped container pins the oldest tag; the next oldest has nothing holding it.
	realPodman(t, "create", "--name", container, tags[0], "true")
	if err := h.PruneImages(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if !imageExists(tags[0]) {
		t.Fatal("prune removed an image a stopped container uses")
	}
	if imageExists(tags[1]) {
		t.Fatal("prune kept a third tag")
	}
	if !imageExists(tags[2]) || !imageExists(tags[3]) {
		t.Fatal("prune removed one of the newest two tags")
	}
	if !imageExists(base) {
		t.Fatal("prune removed the base of images it kept")
	}

	// With the container gone, that tag goes on the next pass.
	realPodman(t, "rm", container)
	if err := h.PruneImages(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if imageExists(tags[0]) {
		t.Fatal("prune kept an image nothing uses")
	}
}
