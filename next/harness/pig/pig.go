// Package pig runs a session of PiG, the Go port of Pi, inside the box agent's own process.
package pig

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/MichaelKinsy/PiG/agent"
	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/coding"
	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/coding/extension/builtin/codemode"
	"github.com/MichaelKinsy/PiG/coding/extension/builtin/toolsearch"
	"github.com/MichaelKinsy/PiG/coding/mcpext"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness"
)

// Config is what a session runs with. AgentDir holds models.json, settings.json, mcp.json, the skills folder and the
// global AGENTS.md, as Pi's agent folder does.
type Config struct {
	Dir          string // the working directory: a session's checkout, or a worker's tree
	AgentDir     string
	SessionDir   string
	Model        contract.ModelChoice
	Instructions string // appended to the system prompt
	// Extensions load ahead of the built-in codemode, tool_search and MCP ones, so a tool of theirs takes the place of a
	// built-in or MCP tool of its name.
	Extensions []extension.InlineExtension
	// Notice hears the warnings an extension gives outside a turn, such as an MCP server that did not connect.
	Notice func(string)
}

// Harness drives one retained PiG session.
type Harness struct {
	// Activity hears what the turn is doing: each tool call, and a compaction.
	Activity func(contract.TurnID, contract.ActivityKind, string)
	services *coding.AgentSessionServices
	session  *coding.Session
	stop     func()
	sends    sync.WaitGroup // the turns' sends, which Close waits for
	done     chan struct{}
	closing  sync.Once

	mu   sync.Mutex
	turn *turn // the turn running now, nil between turns
}

var _ harness.Harness = (*Harness)(nil)

// Open starts the session, continuing the newest one in the session folder if there is one.
func Open(ctx context.Context, c Config) (*Harness, error) {
	extensions := append(append([]extension.InlineExtension{}, c.Extensions...),
		extension.NamedInlineExtension{Name: "codemode", Replaceable: true, Factory: codemode.CreateCodemodeExtension(codemode.Options{})},
		extension.NamedInlineExtension{Name: "tool-search", Replaceable: true, Factory: toolsearch.CreateToolSearchExtensionWith(toolsearch.Options{DeferredToolsSection: true})},
		extension.NamedInlineExtension{Name: "mcp", Replaceable: true, Factory: mcpext.CreateMcpExtension(mcpext.Options{AgentDir: c.AgentDir, ConfigDirName: ".pi", DeferredToolsSection: true})},
	)
	services, err := coding.CreateAgentSessionServices(coding.CreateAgentSessionServicesOptions{
		CWD:      c.Dir,
		AgentDir: c.AgentDir,
		ResourceLoaderOptions: &coding.DefaultResourceLoaderOptions{
			NoExtensions:       true,
			ExtensionFactories: extensions,
			AppendSystemPrompt: []string{c.Instructions},
		},
	})
	if err != nil {
		return nil, err
	}
	h := &Harness{services: services, done: make(chan struct{})}
	if err := h.start(ctx, c); err != nil {
		services.Close()
		return nil, err
	}
	return h, nil
}

func (h *Harness) start(ctx context.Context, c Config) error {
	model, err := coding.BuildModel(string(c.Model.Provider)+"/"+c.Model.ID, h.services)
	if err != nil {
		return err
	}
	resume, err := newest(c.SessionDir)
	if err != nil {
		return err
	}
	manager, err := coding.SessionManagerFor(h.services, coding.SessionStartOptions{SessionDir: c.SessionDir, ResumePath: resume})
	if err != nil {
		return err
	}
	// "+bash" keeps the extension's bash active in a resumed session, which restores the tools its transcript recorded.
	created, err := coding.CreateAgentSessionFromServices(coding.CreateAgentSessionFromServicesOptions{Services: h.services, SessionManager: manager, Model: model, ThinkingLevel: ai.ModelThinkingLevel(c.Model.Thinking), Tools: []string{"+codemode", "+bash"}, ContextUpdates: true})
	if err != nil {
		return err
	}
	h.session = created.Session
	// The listener sees every event, in order, before Prompt returns. Nothing reads the buffered event channel, which
	// would otherwise fill during a long turn and hold the agent loop.
	h.session.DiscardEvents()
	h.stop = h.session.Subscribe(func(e agent.AgentEvent) {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.turn != nil {
			h.turn.event(e)
		}
	})
	var ui extension.UIContext = extension.NoopUIContext
	if c.Notice != nil {
		ui = notices{UIContext: extension.NoopUIContext, notice: c.Notice}
	}
	if err := h.session.BindExtensions(ctx, coding.ExtensionBindings{UIContext: ui}); err != nil {
		h.stop()
		return errors.Join(err, h.session.Close())
	}
	return nil
}

// notices passes on an extension's warnings and errors. The box answers no dialog.
type notices struct {
	extension.UIContext
	notice func(string)
}

func (n notices) Notify(message, kind string) {
	if kind != "info" {
		n.notice(strings.TrimSuffix(message, "\nRun /mcp to fix."))
	}
}

// newest is the session file a box continues: the one written last, or none in a new box.
func newest(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	var path string
	var latest time.Time
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return "", err
		}
		if info.ModTime().After(latest) {
			path, latest = filepath.Join(dir, entry.Name()), info.ModTime()
		}
	}
	return path, nil
}

// Start sends the first input as the prompt and queues the rest as steers, which the agent reads before its first
// model request, so each is its own user message in that request. Each message names its delivery in the session file.
func (h *Harness) Start(ctx context.Context, id contract.TurnID, inputs []contract.TurnInput, started func()) (contract.TurnEnded, error) {
	if len(inputs) == 0 {
		return contract.TurnEnded{Turn: id}, errors.New("a turn needs at least one input")
	}
	return h.run(ctx, id, func(ctx context.Context) error {
		var queued error
		err := h.session.Prompt(ctx, inputs[0].Text, &coding.PromptOptions{Members: members(inputs[0]), PreflightResult: func(coding.PromptDisposition) {
			for _, input := range inputs[1:] {
				if _, err := h.session.Steer(ctx, input.Text, nil, &coding.QueueInputOptions{Members: members(input)}); err != nil {
					queued = errors.Join(queued, err)
				}
			}
			started()
		}})
		return errors.Join(err, queued)
	})
}

// Continue finishes the turn a stopped box agent left running, from what the session file holds, without sending its
// inputs again. A tool call that has no result is not run again.
func (h *Harness) Continue(ctx context.Context, id contract.TurnID, started func()) (contract.TurnEnded, error) {
	return h.run(ctx, id, func(ctx context.Context) error {
		return h.session.ContinueRecovered(ctx, coding.ContinueOptions{Interrupted: interrupted, Started: started})
	})
}

// interrupted is the result of a tool call that was running when the box agent stopped.
const interrupted = "interrupted: the session restarted while this call ran, so whether it finished is unknown, and it is not run again. A command it started on another machine can still be running there"

// run sends a turn and returns once the session settles. The send outlives a cancelled ctx, as a Pi process does, and
// Abort is what stops it.
func (h *Harness) run(ctx context.Context, id contract.TurnID, send func(context.Context) error) (contract.TurnEnded, error) {
	activity := func(contract.TurnID, contract.ActivityKind, string) {}
	if h.Activity != nil {
		activity = h.Activity
	}
	t := newTurn(id, activity)
	h.mu.Lock()
	h.turn = t
	h.mu.Unlock()
	sent := make(chan error, 1)
	h.sends.Go(func() { sent <- send(context.WithoutCancel(ctx)) })
	var err error
	select {
	case err = <-sent:
	case <-ctx.Done():
		err = ctx.Err()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.turn = nil
	return t.result, err
}

func (h *Harness) Steer(ctx context.Context, input contract.TurnInput) error {
	_, err := h.session.Steer(ctx, input.Text, nil, &coding.QueueInputOptions{Members: members(input)})
	return err
}

func (h *Harness) FollowUp(ctx context.Context, input contract.TurnInput) error {
	_, err := h.session.FollowUp(ctx, input.Text, nil, &coding.QueueInputOptions{Members: members(input)})
	return err
}

// TransferModel moves the session to the model, thinking level and compaction trigger. PiG checks the conversation
// against the model and compacts it on the one that held it first when it must (coding.Session.TransferModel). Abort
// cancels that compaction, and the model stays.
func (h *Harness) TransferModel(ctx context.Context, id contract.TurnID, model contract.ModelChoice, compactTokens int) (harness.ModelTransfer, error) {
	if h.Activity != nil {
		h.Activity(id, contract.ActivityModel, "changing model")
	}
	target, err := coding.BuildModel(string(model.Provider)+"/"+model.ID, h.services)
	// A model the box cannot use is the agent program's refusal, which fails the turn; TransferModel's own errors do not.
	if err == nil {
		err = h.session.CheckModelAuth(target)
	}
	if err != nil {
		return harness.ModelTransfer{}, fmt.Errorf("%w: %v", harness.ErrRefused, err)
	}
	result, err := h.session.TransferModel(ctx, target, coding.ModelTransferOptions{ThinkingLevel: ai.ModelThinkingLevel(model.Thinking), CompactionTriggerTokens: compactTokens})
	return harness.ModelTransfer{Path: result.Path, Reason: result.Reason, TokensBefore: int64(result.TokensBefore), TokensAfter: int64(result.TokensAfter)}, err
}

// Compact summarizes the conversation when the session's own measure of its context is over the limit. The session
// knows no size right after a compaction, so a second ask does nothing.
func (h *Harness) Compact(ctx context.Context, over int64) (harness.Compaction, error) {
	usage := h.session.ContextUsage()
	if usage == nil || usage.Tokens == nil || int64(*usage.Tokens) <= over {
		return harness.Compaction{}, nil
	}
	result, err := h.session.Compact(ctx, "")
	if err != nil {
		return harness.Compaction{}, err
	}
	compaction := harness.Compaction{Compacted: true, TokensBefore: int64(result.TokensBefore), TokensAfter: int64(result.EstimatedTokensAfter)}
	if u := result.Usage; u != nil {
		compaction.Usage = contract.Usage{InputTokens: int64(u.Input), OutputTokens: int64(u.Output), CacheReadTokens: int64(u.CacheRead), CacheWriteTokens: int64(u.CacheWrite)}
	}
	return compaction, nil
}

// SetPromptSections sets the turn's context sections, after the ones PiG builds. The first request holds them in its
// system prompt, and a later change reaches the model as a context update before the next request (dyna patch P5).
func (h *Harness) SetPromptSections(_ context.Context, sections []contract.ContextSection) error {
	host := make(ai.OrderedSections, 0, len(sections))
	for _, section := range sections {
		host = append(host, ai.PromptSection{Name: string(section.Key), Value: &section.Text})
	}
	return h.session.SetHostPromptSections(host)
}

func (h *Harness) Abort(ctx context.Context) error { return h.session.Abort(ctx) }

func (h *Harness) SessionFile() string { return h.session.Path() }

// Done closes when the session closes.
func (h *Harness) Done() <-chan struct{} { return h.done }

func (h *Harness) Close() error {
	var err error
	h.closing.Do(func() {
		h.stop()
		err = h.session.Close()
		h.sends.Wait()
		h.services.Close()
		close(h.done)
	})
	return err
}
