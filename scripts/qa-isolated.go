// Run inside the disposable QA sandbox; the shell launcher builds this program.
package main

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"dyna-agent/internal/computers"
	"dyna-agent/internal/database"
	"dyna-agent/internal/websvc/devicestore"
)

//go:embed qa-headless.mjs
var qaHeadless string

func main() {
	steps := flag.String("steps", "", "QA steps JSON")
	web := flag.String("web", "", "built web directory")
	status := flag.String("status", "", "QA intent")
	_ = flag.Bool("isolated", false, "run in an isolated engine")
	flag.Parse()
	if *steps == "" {
		fmt.Fprintln(os.Stderr, "isolated QA requires --steps")
		os.Exit(2)
	}
	if *web != "web/dist" {
		fmt.Fprintln(os.Stderr, "isolated QA requires --web web/dist (embedded in the branch engine)")
		os.Exit(2)
	}
	if _, err := computers.RequiredStatus(*status); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := runIsolated(ctx, *steps); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runIsolated(ctx context.Context, stepsPath string) error {
	dataDir := os.Getenv("DYNA_DATA_DIR")
	// This process owns schema creation only in the disposable sandbox.
	database.OwnSchema()
	if err := qaSeedFixtures(dataDir); err != nil {
		return fmt.Errorf("seed QA fixtures: %w", err)
	}
	engine := exec.CommandContext(ctx, os.Getenv("DYNA_QA_ENGINE"), "engine", "run",
		"--addr", "127.0.0.1:7457", "--card-addr", "127.0.0.1:7458",
		"--no-tunnel", "--no-tailscale", "--no-push", "--allow-agent")
	out, err := engine.StdoutPipe()
	if err != nil {
		return err
	}
	engine.Stderr = os.Stderr
	if err := engine.Start(); err != nil {
		return fmt.Errorf("start QA engine: %w", err)
	}
	defer func() {
		_ = engine.Process.Kill()
		_ = engine.Wait()
	}()
	ready := make(chan error, 1)
	go func() {
		scan := bufio.NewScanner(out)
		for scan.Scan() {
			// Engine output contains a pairing code. Never forward it.
			if strings.HasPrefix(scan.Text(), "Dyna web app on ") {
				ready <- nil
				for scan.Scan() {
				}
				return
			}
		}
		ready <- fmt.Errorf("QA engine stopped before readiness: %v", scan.Err())
	}()
	select {
	case err := <-ready:
		if err != nil {
			return err
		}
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(30 * time.Second):
		return fmt.Errorf("QA engine did not become ready")
	}
	devices, err := devicestore.OpenStore(filepath.Join(dataDir, "web"))
	if err != nil {
		return err
	}
	token, _, err := devices.IssueDevice("browser-qa", "QA browser", time.Hour)
	if err != nil {
		return err
	}
	steps := json.RawMessage(`[]`)
	if stepsPath != "" {
		steps, err = os.ReadFile(stepsPath)
		if err != nil {
			return err
		}
		if !json.Valid(steps) {
			return fmt.Errorf("invalid QA steps JSON")
		}
	}
	config, err := json.Marshal(struct {
		Module, Chromium, Origin, Cookie string
		Steps                            json.RawMessage
	}{os.Getenv("DYNA_QA_MODULE"), os.Getenv("DYNA_QA_CHROMIUM"), "http://127.0.0.1:7457", token, steps})
	if err != nil {
		return err
	}
	script := filepath.Join(os.Getenv("DYNA_SCRATCH"), "qa-headless.mjs")
	if err := os.WriteFile(script, []byte(qaHeadless), 0600); err != nil {
		return err
	}
	browser := exec.CommandContext(ctx, os.Getenv("DYNA_QA_NODE"), script)
	browser.Stdin = bytes.NewReader(config)
	browser.Stdout, browser.Stderr = os.Stdout, os.Stderr
	return browser.Run()
}
