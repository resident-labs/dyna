// Run from the repository root: bash ./scripts/qa-branch.sh --web web/dist --status "Check branch".
package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
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

type qaViewport struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type qaTyping struct {
	Selector string `json:"selector"`
	Text     string `json:"text"`
}

type qaStep struct {
	Viewport   *qaViewport `json:"viewport,omitempty"`
	Goto       *string     `json:"goto,omitempty"`
	Click      *string     `json:"click,omitempty"`
	Type       *qaTyping   `json:"type,omitempty"`
	WaitFor    *string     `json:"waitFor,omitempty"`
	Screenshot *string     `json:"screenshot,omitempty"`
}

func qaUsage() {
	out := flag.CommandLine.Output()
	fmt.Fprint(out, `Usage: bash ./scripts/qa-branch.sh --web DIR --status INTENT [--steps FILE]

Build and open DIR in a dedicated read-only QA browser context.
The ready output prints the page's CDP target id and websocket URL.
Another script can use them to drive the page while this command runs.

With --steps, FILE holds a JSON list and the command exits after running it.
Each list item has exactly one action:
  {"viewport":{"width":1280,"height":800}}
  {"goto":"/rooms"}
  {"click":"button.save"}
  {"type":{"selector":"input[name=name]","text":"QA"}}
  {"waitFor":"text=Ready"}
  {"screenshot":"/absolute/or/relative/path.png"}

A goto is a path or URL on http://127.0.0.1:7457. waitFor waits for a visible
selector. Screenshot paths are relative to the repository root when not absolute.
Without --steps, the command stays running until Ctrl+C or the QA tab closes.
`)
}

func main() {
	if os.Getenv("DYNA_QA_READY") != "1" {
		fmt.Fprintln(os.Stderr, "use bash ./scripts/qa-branch.sh so QA builds take a laptop slot")
		os.Exit(2)
	}
	web := flag.String("web", "", "built branch web directory")
	status := flag.String("status", "", "short visible QA intent")
	stepsPath := flag.String("steps", "", "JSON steps file to run before exiting")
	flag.Usage = qaUsage
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runQA(ctx, *web, *status, *stepsPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runQA(ctx context.Context, web, status, stepsPath string) error {
	intent, err := computers.RequiredStatus(status)
	if err != nil {
		return err
	}
	dir, err := qaWebDir(web)
	if err != nil {
		return err
	}
	var steps []qaStep
	if stepsPath != "" {
		data, err := os.ReadFile(stepsPath)
		if err != nil {
			return fmt.Errorf("read QA steps: %w", err)
		}
		steps, err = parseQASteps(bytes.NewReader(data))
		if err != nil {
			return err
		}
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
		Steps       []qaStep
	}{endpoint, module, dir, engine.ReadOnlyWebMethods(), steps})
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

func parseQASteps(input io.Reader) ([]qaStep, error) {
	decoder := json.NewDecoder(input)
	decoder.DisallowUnknownFields()
	var steps []qaStep
	if err := decoder.Decode(&steps); err != nil {
		return nil, fmt.Errorf("parse QA steps: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("parse QA steps: expected one JSON list")
	}
	if len(steps) == 0 {
		return nil, fmt.Errorf("parse QA steps: the list is empty")
	}
	for i := range steps {
		step := &steps[i]
		actions := 0
		if step.Viewport != nil {
			actions++
		}
		if step.Goto != nil {
			actions++
		}
		if step.Click != nil {
			actions++
		}
		if step.Type != nil {
			actions++
		}
		if step.WaitFor != nil {
			actions++
		}
		if step.Screenshot != nil {
			actions++
		}
		if actions != 1 {
			return nil, fmt.Errorf("parse QA steps: step %d has %d actions; want exactly one", i+1, actions)
		}
		switch {
		case step.Viewport != nil && (step.Viewport.Width <= 0 || step.Viewport.Height <= 0):
			return nil, fmt.Errorf("parse QA steps: step %d needs positive viewport dimensions", i+1)
		case step.Goto != nil && *step.Goto == "":
			return nil, fmt.Errorf("parse QA steps: step %d has an empty goto", i+1)
		case step.Click != nil && *step.Click == "":
			return nil, fmt.Errorf("parse QA steps: step %d has an empty click selector", i+1)
		case step.Type != nil && step.Type.Selector == "":
			return nil, fmt.Errorf("parse QA steps: step %d has an empty type selector", i+1)
		case step.WaitFor != nil && *step.WaitFor == "":
			return nil, fmt.Errorf("parse QA steps: step %d has an empty waitFor selector", i+1)
		case step.Screenshot != nil && *step.Screenshot == "":
			return nil, fmt.Errorf("parse QA steps: step %d has an empty screenshot path", i+1)
		}
	}
	return steps, nil
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
