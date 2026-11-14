package computer

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSolidPNG(t *testing.T) {
	im := image.NewRGBA(image.Rect(0, 0, 3, 3))
	encode := func() []byte {
		var b bytes.Buffer
		if err := png.Encode(&b, im); err != nil {
			t.Fatal(err)
		}
		return b.Bytes()
	}
	if !solidPNG(encode()) {
		t.Fatal("black image not detected")
	}
	im.Set(2, 2, color.White)
	if solidPNG(encode()) {
		t.Fatal("different last pixel ignored")
	}
	if solidPNG([]byte("invalid")) {
		t.Fatal("invalid PNG treated as solid")
	}
}

func TestSoftwareRendering(t *testing.T) {
	if os.Getenv("DYNA_RENDER_TEST") == "" {
		t.Skip("DYNA_RENDER_TEST=1 exercises software GL and Vulkan in a real box")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	p := NewProcess()
	p.root = t.TempDir()
	defer p.Close()
	if _, err := p.Start(ctx); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(runtimeDir(p.socket))
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("runtime directory: %v, %v", info, err)
	}
	report := p.Check(ctx)
	t.Log(report)
	if strings.Count(report, "draws non-black, non-solid pixels") != 2 {
		t.Fatal("GL and Vulkan did not both draw")
	}
	before, err := windows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Launch(ctx, "exec vkcube --width 256 --height 256"); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
		now, err := windows(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for id := range now {
			if before[id] {
				continue
			}
			if err := xdotool(ctx, "windowactivate", "--sync", id); err != nil {
				t.Fatal(err)
			}
			time.Sleep(500 * time.Millisecond)
			shot, err := Capture(ctx, p.socket, Rect{0, 0, 1280, 800})
			if err != nil {
				t.Fatal(err)
			}
			if path := os.Getenv("DYNA_RENDER_SCREENSHOT"); path != "" {
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, shot, 0644); err != nil {
					t.Fatal(err)
				}
			}
			return
		}
	}
}
