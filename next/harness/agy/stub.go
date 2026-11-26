// Package agy is a stand-in for the agy harness (PR 1) so the box wiring builds; PR 1's package replaces this file.
package agy

import (
	"context"
	"errors"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness"
)

type MCPServer struct {
	Name, URL string
	Headers   map[string]string
	Command   string
	Args      []string
	Env       map[string]string
}

type AccountError struct{ Kind, Message string }

func (e AccountError) Error() string { return e.Message }

type Config struct {
	Binary, GeminiDir, Dir, SessionDir string
	Model                              contract.ModelChoice
	Instructions, ModelInstructions    string
	MCPServers                         []MCPServer
	New                                bool
	CancelTimeout                      time.Duration
	Notice                             func(string)
	Account                            func(AccountError)
}

type Harness struct {
	harness.Harness
	Activity func(contract.TurnID, contract.ActivityKind, string)
	Finished func(contract.TurnID, contract.ToolFinished)
}

func Open(context.Context, Config) (*Harness, error) { return nil, errors.New("agy harness not built") }
func (*Harness) Done() <-chan struct{}               { return nil }
func (*Harness) SessionFile() string                 { return "" }
