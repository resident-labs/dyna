package computer

import (
	"bytes"
	"context"
	"fmt"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func runtimeDir(socket string) string { return filepath.Join(filepath.Dir(socket), "runtime") }

func desktopEnv(socket string) []string {
	return append(os.Environ(), "DISPLAY="+display, "XDG_RUNTIME_DIR="+runtimeDir(socket), "XDG_SESSION_TYPE=x11", "XDG_CURRENT_DESKTOP=JWM", "DESKTOP_SESSION=jwm", "WAYLAND_DISPLAY=")
}

func solidPNG(data []byte) bool {
	im, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return false
	}
	b := im.Bounds()
	first := im.At(b.Min.X, b.Min.Y)
	r, g, bl, a := first.RGBA()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			rr, gg, bb, aa := im.At(x, y).RGBA()
			if r != rr || g != gg || bl != bb || a != aa {
				return false
			}
		}
	}
	return true
}

// Check draws real clients and reads their interiors through the same VNC path as screenshots.
// It never treats a renderer's successful initialization as proof that pixels reached the display.
func (p *Process) Check(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	report := "X11, TigerVNC Xvnc, JWM (no compositor), " + Size + ". Renderers below."
	for _, probe := range []struct {
		label, command string
		args           []string
	}{{"GL", "glxgears", []string{"-geometry", "256x256"}}, {"Vulkan", "vkcube", []string{"--width", "256", "--height", "256"}}} {
		report += "\n" + probe.label + ": " + p.drawProbe(ctx, probe.command, probe.args)
	}
	cmd := exec.CommandContext(ctx, "glxinfo", "-B")
	cmd.Env = desktopEnv(p.socket)
	if out, err := cmd.Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, "OpenGL renderer string:") {
				report += "\n" + strings.TrimSpace(line)
			}
		}
	}
	// Enumeration needs no presentation surface; the cube above tests WSI.
	cmd = exec.CommandContext(ctx, "vulkaninfo", "--summary")
	cmd.Env = append(desktopEnv(p.socket), "DISPLAY=")
	if out, err := cmd.Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, "deviceName") {
				report += "\nVulkan " + strings.TrimSpace(line)
			}
		}
	}
	return report
}

func (p *Process) drawProbe(ctx context.Context, name string, args []string) (result string) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	before, err := windows(ctx)
	if err != nil {
		return err.Error()
	}
	var output bytes.Buffer
	p.mu.Lock()
	app, err := p.spawn(&output, "stdbuf", append([]string{"-oL", name}, args...)...)
	if err == nil {
		if p.apps == nil {
			p.apps = map[*program]struct{}{}
		}
		p.apps[app] = struct{}{}
	}
	p.mu.Unlock()
	if err != nil {
		return err.Error()
	}
	defer func() {
		app.kill()
		<-app.done // Wait also joins the stdout copier before reading its buffer.
		for _, line := range strings.Split(output.String(), "\n") {
			if strings.HasPrefix(line, "Selected GPU ") {
				result += "; " + line
				break
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return "failed: no non-solid client image in 8 s"
		case <-app.done:
			return "failed: test app exited"
		case <-time.After(100 * time.Millisecond):
		}
		now, err := windows(ctx)
		if err != nil {
			return err.Error()
		}
		for id := range now {
			if before[id] {
				continue
			}
			region, err := windowRect(ctx, id)
			if err != nil || region.Width != 256 || region.Height != 256 {
				continue // Chromium may finish opening concurrently with the first probe.
			}
			if err := xdotool(ctx, "windowactivate", "--sync", id); err != nil {
				return err.Error()
			}
			// Ignore the client edges as well as the WM decorations.
			region.X += 8
			region.Y += 8
			region.Width -= 16
			region.Height -= 16
			if region.empty() {
				continue
			}
			select {
			case <-ctx.Done():
				return ctx.Err().Error()
			case <-time.After(300 * time.Millisecond):
			}
			shot, err := Capture(ctx, p.socket, region)
			if err != nil {
				return err.Error()
			}
			if !solidPNG(shot) {
				return fmt.Sprintf("draws non-black, non-solid pixels (%s)", name)
			}
		}
	}
}

// windowRect uses absolute client coordinates; xdotool's geometry can count a
// reparenting WM's frame offset twice. Screenshots read only the visible desktop.
func windowRect(ctx context.Context, id string) (Rect, error) {
	cmd := exec.CommandContext(ctx, "xwininfo", "-id", id)
	cmd.Env = append(desktopEnv(VNCSocket), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		return Rect{}, fmt.Errorf("window %s: %w", id, err)
	}
	values := map[string]int{}
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		switch k {
		case "Absolute upper-left X", "Absolute upper-left Y", "Width", "Height":
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				return Rect{}, err
			}
			values[k] = n
		}
	}
	if len(values) != 4 {
		return Rect{}, fmt.Errorf("window %s has no geometry", id)
	}
	x, y := values["Absolute upper-left X"], values["Absolute upper-left Y"]
	width, height := desktopSize()
	right, bottom := min(width, x+values["Width"]), min(height, y+values["Height"])
	x, y = max(0, x), max(0, y)
	region := Rect{x, y, right - x, bottom - y}
	if region.empty() {
		return Rect{}, fmt.Errorf("window %s is outside the desktop", id)
	}
	return region, nil
}
