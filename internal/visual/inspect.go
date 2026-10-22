// Package visual renders HTML and checks its visible content in Chromium.
package visual

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"dyna-agent/internal/computers"
	"dyna-agent/internal/room"
)

// ErrNoBrowser means no configured or discovered Chromium executable is available.
var ErrNoBrowser = errors.New("no Chromium browser found")

// Check reports one quality check; the room stores it with the card.
type Check = room.Check

// Shot is a half-size viewport screenshot at the original Width.
type Shot struct {
	Width int
	Image []byte
}

// Result contains the banner, thumbnail, width screenshots, and five ordered checks.
type Result struct {
	Banner []byte // WebP, 960 by 360 pixels.
	Thumb  []byte // WebP, 640 by 480 pixels.
	Checks []Check
	Widths []Shot
}

// Inspect renders doc with csp and permits external HTTPS requests only with allowNet.
func Inspect(ctx context.Context, doc []byte, csp string, allowNet bool) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	binary, err := computers.ChromiumExecutable()
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		return Result{}, ErrNoBrowser
	}
	if err != nil {
		return Result{}, err
	}
	dir, err := os.MkdirTemp("", "dyna-visual-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(dir)
	readChild, writeParent, err := os.Pipe()
	if err != nil {
		return Result{}, err
	}
	defer readChild.Close()
	defer writeParent.Close()
	readParent, writeChild, err := os.Pipe()
	if err != nil {
		return Result{}, err
	}
	defer readParent.Close()
	defer writeChild.Close()
	args := []string{"--headless", "--remote-debugging-pipe", "--no-first-run", "--no-default-browser-check", "--disable-gpu", "--hide-scrollbars", "--mute-audio", "--disable-background-networking", "--disable-component-update", "--disable-sync", "--disable-extensions", "--disable-features=Translate,MediaRouter", "--user-data-dir=" + dir}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.ExtraFiles = []*os.File{readChild, writeChild}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return Result{}, err
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	var c *client
	defer func() {
		// Closing cleanly lets Chromium reap its renderers before the browser exits.
		if c != nil && ctx.Err() == nil {
			c.session = ""
			_, _ = c.send("Browser.close", map[string]any{})
			select {
			case <-exited:
			case <-time.After(250 * time.Millisecond):
			}
		}
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-exited
	}()
	_ = readChild.Close()
	_ = writeChild.Close()
	c = &client{input: writeParent, messages: make(chan message, 64), readError: make(chan error, 1), doc: doc, csp: csp, allowNet: allowNet, hosts: map[string]bool{}}
	go c.read(ctx, readParent)
	var target struct{ TargetID string }
	if err = c.call(ctx, "Target.createTarget", map[string]any{"url": "about:blank"}, &target); err != nil {
		return Result{}, err
	}
	var attached struct{ SessionID string }
	if err = c.call(ctx, "Target.attachToTarget", map[string]any{"targetId": target.TargetID, "flatten": true}, &attached); err != nil {
		return Result{}, err
	}
	c.session = attached.SessionID
	for _, method := range []string{"Page.enable", "Runtime.enable", "Network.enable", "Fetch.enable"} {
		if err = c.call(ctx, method, map[string]any{}, nil); err != nil {
			return Result{}, err
		}
	}
	if err = c.call(ctx, "Runtime.addBinding", map[string]any{"name": policyBinding}, nil); err != nil {
		return Result{}, err
	}
	if err = c.call(ctx, "Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": policyScript}, nil); err != nil {
		return Result{}, err
	}
	metrics := func(w int) error {
		return c.call(ctx, "Emulation.setDeviceMetricsOverride", map[string]any{"width": w, "height": 800, "deviceScaleFactor": 1, "mobile": false}, nil)
	}
	if err = metrics(1280); err != nil {
		return Result{}, err
	}
	c.loaded = false
	if err = c.call(ctx, "Page.navigate", map[string]any{"url": "https://visual.invalid/"}, nil); err != nil {
		return Result{}, err
	}
	loadCtx, loadCancel := context.WithTimeout(ctx, 8*time.Second)
	defer loadCancel()
	for !c.loaded {
		m, e := c.next(loadCtx)
		if e != nil {
			if errors.Is(e, context.DeadlineExceeded) && ctx.Err() == nil {
				break
			}
			return Result{}, e
		}
		if err = c.event(m); err != nil {
			return Result{}, err
		}
	}
	r := Result{Checks: []Check{{Name: "renders", Status: "pass"}, {Name: "console", Status: "pass"}, {Name: "widths", Status: "pass"}, {Name: "contrast", Status: "pass"}, {Name: "network", Status: "pass"}}}
	var visible bool
	if err = c.evaluate(ctx, visibleContentScript, &visible); err != nil {
		return Result{}, err
	}
	if !c.loaded || !visible {
		r.Checks[0].Status = "fail"
		r.Checks[0].Detail = "Page did not load visible content"
	}
	var widths []int
	r.Widths = make([]Shot, 3)
	for _, index := range []int{2, 0, 1} {
		w := []int{360, 768, 1280}[index]
		if index != 2 {
			if err = metrics(w); err != nil {
				return Result{}, err
			}
		}
		var overflow bool
		if err = c.evaluate(ctx, `document.documentElement.scrollWidth > innerWidth + 1`, &overflow); err != nil {
			return Result{}, err
		}
		if overflow {
			widths = append(widths, w)
		}
		image, err := c.screenshot(ctx, w, 800, .5)
		if err != nil {
			return Result{}, err
		}
		r.Widths[index] = Shot{Width: w, Image: image}
	}
	if err = metrics(1280); err != nil {
		return Result{}, err
	}
	if len(widths) > 0 {
		sort.Ints(widths)
		var labels []string
		for _, w := range widths {
			labels = append(labels, fmt.Sprint(w))
		}
		r.Checks[2].Status = "warn"
		r.Checks[2].Detail = "Overflow at widths: " + strings.Join(labels, ", ")
	}
	var contrast struct {
		Count  int
		Sample string
	}
	if err = c.evaluate(ctx, contrastScript, &contrast); err != nil {
		return Result{}, err
	}
	if contrast.Count > 0 {
		r.Checks[3].Status = "warn"
		r.Checks[3].Detail = short(fmt.Sprintf("%d text elements below contrast threshold: %s", contrast.Count, contrast.Sample))
	}
	for i, s := range []struct {
		height int
		scale  float64
	}{{480, .75}, {960, .5}} {
		b, err := c.screenshot(ctx, 1280, s.height, s.scale)
		if err != nil {
			return Result{}, err
		}
		if i == 0 {
			r.Banner = b
		} else {
			r.Thumb = b
		}
	}
	if len(c.console) > 0 {
		r.Checks[1].Status = "fail"
		r.Checks[1].Detail = short(fmt.Sprintf("%d errors: %s", len(c.console), c.console[0]))
	}
	var hosts []string
	for host := range c.hosts {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	if len(hosts) > 0 {
		if !allowNet {
			r.Checks[4].Status = "fail"
		}
		if len(hosts) > 3 {
			hosts = hosts[:3]
		}
		r.Checks[4].Detail = short(strings.Join(hosts, ", "))
	}
	return r, nil
}
func short(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 200 {
		s = s[:200]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}
