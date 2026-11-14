package agent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// agentToolCalls is how many tool calls one turn of an in-box agent may make. At the cap the agent is told to answer, and
// one more request is allowed for it.
const agentToolCalls = 40

const agentCapNotice = "You have used all 40 of your tool calls. Do not call another tool: answer now with what you have found, and say what you did not get to."

// agentSettle is how long a stopped agent has to settle its streaming message before its turn is given up.
var agentSettle = 5 * time.Second

// agentSettleFor sets the bound for a test, and returns the old one.
func agentSettleFor(d time.Duration) time.Duration {
	old := agentSettle
	agentSettle = d
	return old
}

var errAgentCap = errors.New("the agent kept calling tools after it was told to answer")

// agentSession is one open conversation of an in-box agent. harness/pig's Harness is one.
type agentSession interface {
	Start(context.Context, contract.TurnID, []contract.TurnInput, func()) (contract.TurnEnded, error)
	Steer(context.Context, contract.TurnInput) error
	Abort(context.Context) error
	SessionFile() string
	SetBeforeRequest(func(context.Context) error)
	Close() error
}

// agentEngine is what in-box agents need from the box socket.
type agentEngine interface {
	AgentStarted(context.Context, contract.AgentStartedRequest) error
	AgentEnded(context.Context, contract.AgentEndedRequest) error
}

// AgentsConfig is what a box hands its in-box agents.
type AgentsConfig struct {
	Engine agentEngine
	// Open opens the conversation in dir, a folder of its own that holds its session files: it continues the newest one there, or
	// starts one. toolCall is told each tool call the session starts.
	Open       func(ctx context.Context, turn contract.AgentTurn, dir string, toolCall func()) (agentSession, error)
	Transcript *Transcript
	Sessions   string // PiG's session folder, which holds each agent's conversations under agents/<name>/<generation>/
	// Orient is the text an agent that asks for it reads ahead of its prompt: where its parent's work is.
	Orient func(context.Context) string
}

// Agents runs in-box agents beside the session's own turn. An agent has no identity: whatever it does, it does as the session
// whose box this is, so its tool list is all its authority. One turn per name runs at a time, and a second waits.
type Agents struct {
	ctx    context.Context
	config AgentsConfig

	mu      sync.Mutex
	running map[contract.AgentID]context.CancelFunc
	names   map[string]chan struct{}
	slots   map[contract.ProfileName]chan struct{}
	// current is each name's conversation, a generation folder. A generation that a turn gave up on is in abandoned, and its
	// session may still be writing, so nothing resumes it and the next turn of its name starts a new one.
	current   map[string]string
	abandoned map[string]bool // by name and generation
}

// abandonedMarker is the file a turn leaves in the folder of a conversation it gave up on, so that a restarted box does not resume it.
const abandonedMarker = "abandoned"

// NewAgents starts the box's agents. It first clears what an earlier run left: the conversations it gave up on, and every generation older
// than a name's newest. Nothing of those is still writing, since the process that wrote them is gone.
func NewAgents(ctx context.Context, config AgentsConfig) *Agents {
	root := filepath.Join(config.Sessions, "agents")
	names, _ := os.ReadDir(root)
	for _, name := range names {
		kept := ""
		generations, _ := os.ReadDir(filepath.Join(root, name.Name())) // oldest first
		for _, generation := range generations {
			folder := filepath.Join(root, name.Name(), generation.Name())
			if _, err := os.Stat(filepath.Join(folder, abandonedMarker)); err == nil {
				_ = os.RemoveAll(folder)
				continue
			}
			if kept != "" {
				_ = os.RemoveAll(filepath.Join(root, name.Name(), kept))
			}
			kept = generation.Name()
		}
	}
	return &Agents{ctx: ctx, config: config, running: make(map[contract.AgentID]context.CancelFunc), names: make(map[string]chan struct{}), slots: make(map[contract.ProfileName]chan struct{}), current: make(map[string]string), abandoned: make(map[string]bool)}
}

// conversation is the folder the turn's conversation lives in. A persisting turn continues its name's current generation, or on a
// first turn since the box started the newest one on disk; otherwise it starts a new generation, named by the time so that they sort.
func (a *Agents) conversation(turn contract.AgentTurn) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	folder := filepath.Join(a.config.Sessions, "agents", turn.Name)
	if turn.Persist {
		generation, known := a.current[turn.Name]
		if !known {
			if generations, _ := os.ReadDir(folder); len(generations) > 0 {
				generation = generations[len(generations)-1].Name()
			}
		}
		if generation != "" && !a.abandoned[turn.Name+"/"+generation] {
			a.current[turn.Name] = generation
			return filepath.Join(folder, generation)
		}
	}
	generation := fmt.Sprintf("%020d", time.Now().UnixNano())
	if turn.Persist {
		a.current[turn.Name] = generation
	}
	return filepath.Join(folder, generation)
}

// abandon marks the conversation as given up on, in memory first, which cannot fail, and then with a file in its folder.
func (a *Agents) abandon(name, folder string) {
	a.mu.Lock()
	a.abandoned[name+"/"+filepath.Base(folder)] = true
	a.mu.Unlock()
	if err := os.WriteFile(filepath.Join(folder, abandonedMarker), nil, 0600); err != nil {
		log.Printf("agent %s: marking its conversation abandoned: %v", name, err)
	}
}

// Files lists every agent session file in the box, for the transcript to send, abandoned conversations too: their sessions may still write.
func (a *Agents) Files() []string {
	files, _ := filepath.Glob(filepath.Join(a.config.Sessions, "agents", "*", "*", "*.jsonl"))
	return files
}

// Turn starts the turn and returns at once. The answer is agent.ended, which every turn sends, however it ends.
func (a *Agents) Turn(turn contract.AgentTurn) {
	ctx, cancel := context.WithCancel(a.ctx)
	if turn.TimeoutMS > 0 {
		cancel()
		ctx, cancel = context.WithTimeout(a.ctx, time.Duration(turn.TimeoutMS)*time.Millisecond)
	}
	a.mu.Lock()
	if _, ok := a.running[turn.ID]; ok {
		a.mu.Unlock()
		cancel()
		return
	}
	a.running[turn.ID] = cancel
	if a.names[turn.Name] == nil {
		a.names[turn.Name] = make(chan struct{}, 1)
	}
	name := a.names[turn.Name]
	if turn.Profile.MaxConcurrent > 0 && a.slots[turn.Profile.Name] == nil {
		a.slots[turn.Profile.Name] = make(chan struct{}, turn.Profile.MaxConcurrent)
	}
	slots := a.slots[turn.Profile.Name]
	a.mu.Unlock()
	go func() {
		defer func() {
			cancel()
			a.mu.Lock()
			delete(a.running, turn.ID)
			a.mu.Unlock()
		}()
		ended := a.run(ctx, turn, name, slots)
		ended.ID = turn.ID
		// The report outlives a stop, which is what it reports.
		if err := a.config.Engine.AgentEnded(context.WithoutCancel(ctx), ended); err != nil {
			log.Printf("agent %s ended: %v", turn.ID, err)
		}
	}()
}

// Stop ends the turn. Its answer still comes, with the text the agent had.
func (a *Agents) Stop(id contract.AgentID) {
	a.mu.Lock()
	cancel := a.running[id]
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *Agents) run(ctx context.Context, turn contract.AgentTurn, name, slots chan struct{}) (ended contract.AgentEndedRequest) {
	ended.Outcome = contract.TurnFailed
	for _, held := range []chan struct{}{name, slots} {
		if held == nil {
			continue
		}
		select {
		case held <- struct{}{}:
			defer func() { <-held }()
		case <-ctx.Done():
			ended.Outcome = contract.TurnStopped
			return ended
		}
	}
	dir := a.conversation(turn)
	if err := os.MkdirAll(dir, 0700); err != nil {
		ended.Error = err.Error()
		return ended
	}
	existing, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	ended.Fresh = !turn.Persist || len(existing) == 0
	var calls atomic.Int32
	session, err := a.config.Open(ctx, turn, dir, func() { calls.Add(1) })
	if err != nil {
		ended.Error = "the agent did not start: " + err.Error()
		return ended
	}
	file := session.SessionFile()
	// A turn or a session that will not settle does not hold the name or the slot: it is left to finish in the background. Its
	// conversation is marked abandoned first, so the next turn of the name starts a new one in a folder of its own.
	var abandoned atomic.Bool
	defer func() {
		closed := make(chan struct{})
		go func() {
			_ = session.Close()
			a.config.Transcript.syncLogged(context.WithoutCancel(ctx), file)
			close(closed)
		}()
		select {
		case <-closed:
		case <-time.After(agentSettle):
			log.Printf("agent %s: its session is still closing; it closes in the background", turn.ID)
			abandoned.Store(true)
		}
		if abandoned.Load() {
			a.abandon(turn.Name, dir)
		}
	}()
	var offset int64
	if info, err := os.Stat(file); err == nil {
		offset = info.Size()
	}
	path, _ := filepath.Rel(a.config.Sessions, file)
	if err := a.config.Engine.AgentStarted(ctx, contract.AgentStartedRequest{ID: turn.ID, Transcript: path, Offset: offset}); err != nil {
		log.Printf("agent %s started: %v", turn.ID, err) // only the live view needs it
	}
	var told atomic.Int32 // the call count when the agent was told to answer, or 0
	var capped atomic.Bool
	session.SetBeforeRequest(func(ctx context.Context) error {
		switch n := calls.Load(); {
		case told.Load() > 0 && n > told.Load():
			capped.Store(true)
			return errAgentCap
		case n >= agentToolCalls && told.Load() == 0:
			told.Store(n)
			return session.Steer(ctx, contract.TurnInput{Delivery: turn.Delivery, Text: agentCapNotice})
		}
		return nil
	})
	prompt := turn.Prompt
	if ended.Fresh && turn.FreshPrompt != "" {
		prompt = turn.FreshPrompt
	}
	if turn.Orient && a.config.Orient != nil {
		prompt = a.config.Orient(ctx) + "\n\n" + prompt
	}
	// The turn runs on a context a stop cannot cancel: a cancelled one would return the turn before Abort has settled the message it was
	// streaming, and the text so far would be lost. A stop aborts instead, and the turn settles with that text.
	type started struct {
		result contract.TurnEnded
		err    error
	}
	done := make(chan started, 1)
	go func() {
		result, err := session.Start(context.WithoutCancel(ctx), turn.Turn, []contract.TurnInput{{Delivery: turn.Delivery, Text: prompt}}, func() {})
		done <- started{result, err}
	}()
	var finished started
	select {
	case finished = <-done:
	case <-ctx.Done():
		// Abort waits for the session to be idle, so it runs apart: the bound covers it and the settling of the turn together.
		go func() { _ = session.Abort(context.WithoutCancel(ctx)) }()
		select {
		case finished = <-done:
		case <-time.After(agentSettle):
			log.Printf("agent %s: its turn did not settle after the abort; it is reported with the text it had", turn.ID)
			abandoned.Store(true)
		}
	}
	result, err := finished.result, finished.err
	ended.Final, ended.Usage = result.Final, result.Usage
	switch {
	case ctx.Err() != nil:
		ended.Outcome = contract.TurnStopped
		if errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
			ended.Error = "the agent ran out of time"
		}
	case capped.Load():
		ended.Outcome, ended.Error = contract.TurnStopped, fmt.Sprintf("it used %d tool calls and was told to answer, and called more", agentToolCalls)
	case err != nil:
		ended.Error = err.Error()
	default:
		ended.Outcome, ended.Error = result.Outcome, result.Error
	}
	return ended
}

// gitBlock is the state of the working repository, as the parent's box has it, which an agent that asks for it reads first.
func gitBlock(ctx context.Context, dir string) string {
	var out strings.Builder
	for _, args := range [][]string{{"rev-parse", "--abbrev-ref", "HEAD"}, {"rev-parse", "HEAD"}, {"status", "--short"}, {"diff", "--stat"}} {
		text, err := runGit(ctx, dir, "", args...)
		if err != nil {
			return ""
		}
		fmt.Fprintf(&out, "$ git %s\n%s\n", strings.Join(args, " "), text)
	}
	return out.String()
}
