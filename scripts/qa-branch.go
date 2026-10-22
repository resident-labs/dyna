// Run from the repository root: bash ./scripts/qa-branch.sh --web web/dist --status "Check branch".
package main

import (
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
	"strconv"
	"strings"
	"syscall"
	"time"

	"dyna-agent/internal/computers"
	"dyna-agent/internal/engine"
)

//go:embed qa-branch.mjs
var browserQAScript string

func main() {
	if os.Getenv("DYNA_QA_READY") != "1" {
		fmt.Fprintln(os.Stderr, "use bash ./scripts/qa-branch.sh so QA builds take a laptop slot")
		os.Exit(2)
	}
	web := flag.String("web", "", "built branch web directory")
	status := flag.String("status", "", "short visible QA intent")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runQA(ctx, *web, *status); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runQA(ctx context.Context, web, status string) error {
	intent, err := computers.RequiredStatus(status)
	if err != nil {
		return err
	}
	dir, err := qaWebDir(web)
	if err != nil {
		return err
	}
	dyna := os.Getenv("DYNA_BIN")
	if dyna == "" {
		dyna = "dyna"
	}
	open := exec.CommandContext(ctx, dyna, "browser", "open", "http://127.0.0.1:7457", "--status", intent)
	if out, err := open.CombinedOutput(); err != nil {
		return fmt.Errorf("open browser: %w: %s", err, out)
	}
	connection, err := exec.CommandContext(ctx, dyna, "browser").Output()
	if err != nil {
		return fmt.Errorf("connect browser: %w", err)
	}
	endpoint, module, err := browserConnection(string(connection))
	if err != nil {
		return err
	}
	config, err := json.Marshal(struct {
		Endpoint    string
		Module      string
		Web         string
		ReadMethods []string
	}{endpoint, module, dir, engine.ReadOnlyWebMethods()})
	if err != nil {
		return err
	}
	child := exec.CommandContext(ctx, "node", "--input-type=module", "-e", browserQAScript)
	child.Stdin = bytes.NewReader(config)
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	child.Cancel = func() error { return child.Process.Signal(syscall.SIGTERM) }
	child.WaitDelay = 3 * time.Second
	return child.Run()
}

func browserConnection(output string) (string, string, error) {
	lines := strings.Split(output, "\n")
	if len(lines) < 2 || !strings.HasPrefix(lines[0], "ws://127.0.0.1:") {
		return "", "", fmt.Errorf("dyna browser returned no loopback CDP endpoint")
	}
	_, imported, ok := strings.Cut(lines[1], "import(")
	if !ok {
		return "", "", fmt.Errorf("dyna browser returned no Playwright module")
	}
	quoted, _, ok := strings.Cut(imported, ");")
	if !ok {
		return "", "", fmt.Errorf("dyna browser returned an invalid module import")
	}
	module, err := strconv.Unquote(quoted)
	if err != nil || !filepath.IsAbs(module) {
		return "", "", fmt.Errorf("dyna browser returned an invalid module path")
	}
	return lines[0], module, nil
}

func qaWebDir(dir string) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("--web needs a built web directory")
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	index, err := os.ReadFile(filepath.Join(absolute, "index.html"))
	if err != nil {
		return "", fmt.Errorf("read branch index: %w", err)
	}
	if !bytes.Contains(index, []byte("</head>")) {
		return "", fmt.Errorf("branch index.html has no </head>")
	}
	return absolute, nil
}
