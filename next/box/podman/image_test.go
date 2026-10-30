package podman

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

type imageRunner struct {
	missing  bool
	commands []string
	input    []byte
}

func (r *imageRunner) Run(_ context.Context, input []byte, args ...string) ([]byte, error) {
	for _, arg := range args {
		if arg == "-O" {
			return nil, nil
		}
	}
	command := args[len(args)-1]
	r.commands = append(r.commands, command)
	if strings.Contains(command, "'image' 'exists'") && r.missing {
		return []byte("DYNA-END 00000000000000000000 001\n"), nil
	}
	r.input = append([]byte(nil), input...)
	return []byte("DYNA-END 00000000000000000000 000\n"), nil
}

func TestBuildBaseSendsTaggedContext(t *testing.T) {
	r := &imageRunner{missing: true}
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	tag, err := h.BuildBase(context.Background(), []byte("container"), []byte("pi"))
	if err != nil {
		t.Fatal(err)
	}
	const want = "dyna-base:5e39aec50ee63e9a28cb339d41a69cef7f83a486e64807583b6f72098a0c4af1"
	if tag != want {
		t.Fatalf("tag %s", tag)
	}
	if len(r.commands) != 2 || !strings.HasSuffix(r.commands[0], "'podman' 'image' 'exists' '"+tag+"'") || !strings.HasSuffix(r.commands[1], "'podman' 'build' '--tag' '"+tag+"' '-'") {
		t.Fatalf("commands %v", r.commands)
	}
	i := bytes.IndexByte(r.input, '\n')
	if i < 0 || string(r.input[:i+1]) != fmt.Sprintf("DYNA-IN %020d\n", len(r.input)-i-1) {
		t.Fatal("invalid input frame")
	}
	tr := tar.NewReader(bytes.NewReader(r.input[i+1:]))
	for _, file := range []struct{ name, content string }{{"Containerfile", "container"}, {"pi.tgz", "pi"}} {
		header, err := tr.Next()
		if err != nil || header.Name != file.name {
			t.Fatalf("header %v: %v", header, err)
		}
		data, err := io.ReadAll(tr)
		if err != nil || string(data) != file.content {
			t.Fatalf("content %q: %v", data, err)
		}
	}
}

type blockedImageRunner struct {
	started chan struct{}
	release chan struct{}
}

func (r *blockedImageRunner) Run(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	for _, arg := range args {
		if arg == "-O" {
			return nil, nil
		}
	}
	close(r.started)
	<-r.release
	return []byte("DYNA-END 00000000000000000000 000\n"), nil
}

func TestBuildBaseCancellationWhileAnotherBuildRuns(t *testing.T) {
	r := &blockedImageRunner{started: make(chan struct{}), release: make(chan struct{})}
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := h.BuildBase(context.Background(), []byte("container"), []byte("pi"))
		done <- err
	}()
	<-r.started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = h.BuildBase(ctx, []byte("container"), []byte("pi"))
	close(r.release)
	if firstErr := <-done; firstErr != nil {
		t.Fatal(firstErr)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestBuildBaseReusesExistingTag(t *testing.T) {
	r := &imageRunner{}
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.BuildBase(context.Background(), []byte("container"), []byte("pi")); err != nil {
		t.Fatal(err)
	}
	if len(r.commands) != 1 || !strings.Contains(r.commands[0], "'image' 'exists'") {
		t.Fatalf("commands %v", r.commands)
	}
}
