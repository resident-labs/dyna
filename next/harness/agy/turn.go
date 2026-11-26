package agy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness"
	"github.com/resident-labs/dyna/harness/agy/acp"
	"github.com/resident-labs/dyna/harness/agy/sessiondb"
)

// T3 ends a prompt that agy leaves open without updates after 15 minutes, the clock suspended while a tool call or an approval
// is open, and holds a settled turn for a background command at most 30 minutes (Adapter:259-264,311-313, quirks 1 and 31). They
// are fields of the Harness so tests can shorten them.
const (
	defaultPromptInactivity = 15 * time.Minute
	defaultHoldMax          = 30 * time.Minute
	defaultWatchEvery       = 10 * time.Second
)

var errInactive = errors.New("agy sent no update for too long; the turn was ended")

// block is one part of the assistant message being built.
type block struct {
	kind string // "text", "thinking" or "toolCall"
	text string
	call map[string]any
}

type call struct {
	name     string
	closed   bool
	outlives bool // a command or schedule agy goes on running after its prompt ends (T3 AntigravityProtocol.ts:60-76)
}

// turn is what a turn's updates build up while it runs. Every field is guarded by Harness.mu.
type turn struct {
	id         contract.TurnID
	open       int // prompts sent and not answered
	closing    bool
	aborted    bool
	lastUpdate time.Time
	calls      map[string]*call
	openCalls  int
	outlive    int  // open calls that outlive the prompt
	answered   bool // agy has gone on from the prompt: a tool call or a thought came (a 404 after that is transient)
	reply      bool // agy streams a reply no prompt asked for; its usage_update ends it
	native     bool // the session's records show a wake or children at work
	settledAt  time.Time
	holdDone   bool
	blocks     []block
	kept, cur  string // answers of earlier steered inputs; text since the last tool call
	last       string // the latest text
	usage      contract.Usage
	fail       *failure
	stopped    bool
	err        error // the connection broke, or the turn went silent
	queued     chan struct{}
	queuedOnce sync.Once
	finished   chan struct{}
	idle       chan struct{} // closed once an aborted turn has no prompt open
	idleOnce   sync.Once
	wake       chan struct{}
	activity   func(contract.TurnID, contract.ActivityKind, string)
	toolFinish func(contract.TurnID, contract.ToolFinished)
}

func joinReply(kept, text string) string {
	if kept == "" || text == "" {
		return kept + text
	}
	return kept + "\n\n" + text
}

// Start sends the inputs as one prompt, and runs held follow-ups after its response, until none are held. It then holds the
// turn while agy still runs a background command or streams a reply nobody asked for (T3's post-settle continuation,
// enablePostSettleContinuation and postSettleCommandHold, quirks 1 and 2), for 30 minutes at most.
func (h *Harness) Start(ctx context.Context, id contract.TurnID, inputs []contract.TurnInput, started func()) (contract.TurnEnded, error) {
	if len(inputs) == 0 {
		return contract.TurnEnded{Turn: id}, errors.New("a turn needs at least one input")
	}
	if err := h.ensure(ctx); err != nil {
		h.doneOnce.Do(func() { close(h.done) })
		return contract.TurnEnded{Turn: id}, err
	}
	h.mu.Lock()
	broken := h.broken
	h.mu.Unlock()
	if broken {
		// The saved conversation fails every prompt (T3 Outcome:53-55, 231-241). T3 starts a fresh session with a portable
		// handoff of the thread's context; that is a later change here, so for now the context is not carried over.
		if err := h.NewConversationForce(ctx); err != nil {
			return contract.TurnEnded{Turn: id}, err
		}
	}
	t := &turn{
		id: id, lastUpdate: time.Now(), calls: map[string]*call{}, queued: make(chan struct{}), finished: make(chan struct{}), idle: make(chan struct{}),
		wake: make(chan struct{}, 1), activity: func(contract.TurnID, contract.ActivityKind, string) {}, toolFinish: h.Finished,
	}
	if h.Activity != nil {
		t.activity = h.Activity
	}
	h.mu.Lock()
	h.turn = t
	h.detached = false
	t.open = 1
	h.mu.Unlock()
	defer close(t.finished)
	if err := h.begin(t, inputs, started); err != nil {
		return h.endTurn(t, err)
	}
	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go h.watch(t, stopWatch)
	t.activity(id, contract.ActivityModelWait, "waiting for model")
	for {
		select {
		case <-t.wake:
		case <-ctx.Done():
			// A cancelled run still leaves agy running its prompt, so cancel it, as Abort does.
			h.cancel(context.Background(), t)
			res, _ := h.endTurn(t, nil)
			return res, ctx.Err()
		}
		h.mu.Lock()
		if t.open > 0 {
			h.mu.Unlock()
			continue
		}
		ended := t.fail != nil || t.stopped || t.err != nil || t.aborted
		if len(h.held) == 0 || ended {
			if !ended && t.settledAt.IsZero() {
				// First look after the response: agy's records may show work that outlives it.
				t.settledAt = time.Now()
				h.mu.Unlock()
				h.checkNative(t)
				t.signal()
				continue
			}
			if !ended && !t.holdDone && t.holding() {
				h.mu.Unlock()
				continue
			}
			t.closing = true
			h.mu.Unlock()
			return h.endTurn(t, nil)
		}
		next := h.held[0]
		h.held = h.held[1:]
		t.kept, t.cur = joinReply(t.kept, t.cur), ""
		t.open++
		t.settledAt = time.Time{}
		h.mu.Unlock()
		if err := h.begin(t, []contract.TurnInput{next}, nil); err != nil {
			return h.endTurn(t, err)
		}
	}
}

// holding is true while the settled turn waits for work agy goes on with. Harness.mu is held.
func (t *turn) holding() bool { return t.outlive > 0 || t.reply || t.native }

// endTurn builds the result and ends the turn: the queue is dropped, as a finished run takes no input.
func (h *Harness) endTurn(t *turn, err error) (contract.TurnEnded, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	t.closing = true
	h.turn, h.held = nil, nil
	// Work agy still runs is left to run: a later message does not cancel it, Stop does (T3 Adapter:265-269, quirk 3).
	h.detached = !t.aborted && t.holding()
	if err == nil {
		err = t.err
	}
	t.flush(h)
	res := contract.TurnEnded{Turn: t.id, Outcome: contract.TurnCompleted, Final: joinReply(t.kept, t.cur), LastText: t.last, Usage: t.usage}
	res.Usage.Model = h.cfg.Model.ID
	switch {
	case t.aborted:
		res.Outcome, res.Final = contract.TurnStopped, ""
		return res, nil
	case err != nil:
		return res, err
	case t.fail != nil:
		res.Outcome, res.Error, res.Cause, res.ModelRetryAfterSeconds = contract.TurnFailed, t.fail.message, t.fail.cause, t.fail.retryAfter
		return res, nil
	case t.stopped:
		res.Outcome, res.Final = contract.TurnStopped, ""
	}
	return res, nil
}

// checkNative asks agy's records whether a wake is queued or native children still work (T3 hasPendingReply and
// subagentsOutlivingPrompt, AntigravityAdapterV2.ts:321-348). It runs unlocked: it reads files.
func (h *Harness) checkNative(t *turn) {
	h.mu.Lock()
	sid := h.sessionID
	var open []string
	for id, c := range t.calls {
		if !c.closed {
			open = append(open, id)
		}
	}
	h.mu.Unlock()
	native := sessiondb.PendingReply(h.cfg.GeminiDir, sid) || len(sessiondb.ChildrenAtWork(h.cfg.GeminiDir, sid, open)) > 0
	h.mu.Lock()
	changed := t.native != native
	t.native = native
	h.mu.Unlock()
	if changed {
		t.signal()
	}
}

// told is what agy has been told in prompts since its process started or its conversation was compacted (T3 adapter.ts
// promptInstructionStates, promptRuntimeInstructions, deliveredAppContext, compactionMarkers). gen changes on a reset, so a
// prompt that was written before it does not mark what it carried as told.
type told struct {
	gen          int
	instructions bool
	context      map[string]string
	runtime      string
}

// promptBlocks builds the blocks of a prompt as T3's resolvePromptParts does (adapter.ts:9980-10075): context entries that
// changed as <external_KEY> blocks before the prompt; the user text, wrapped with the instructions the first time; the
// attachments; and the runtime info after, when its text changed. A native slash command is sent as it is, with nothing
// around it. It returns what to mark as told once the prompt has been answered (adapter.ts:10185-10225).
func (h *Harness) promptBlocks(inputs []contract.TurnInput, p *process) ([]map[string]any, func(), error) {
	h.mu.Lock()
	state := h.told
	current := h.current()
	model := h.cfg.Model
	h.mu.Unlock()
	slash := strings.HasPrefix(strings.TrimSpace(inputs[0].Text), "/")
	var blocks []map[string]any
	next := told{gen: state.gen, instructions: state.instructions, context: map[string]string{}, runtime: state.runtime}
	for k, v := range state.context {
		next.context[k] = v
	}
	if !slash {
		for _, s := range current {
			if state.context[string(s.Key)] != s.Text {
				blocks = append(blocks, map[string]any{"type": "text", "text": "<external_" + string(s.Key) + ">" + s.Text + "</external_" + string(s.Key) + ">"})
				next.context[string(s.Key)] = s.Text
			}
		}
	}
	instructions := ""
	if !slash && !state.instructions {
		parts := slices.DeleteFunc(append([]string{h.cfg.Instructions}, h.cfg.ProjectInstructions...), func(s string) bool { return s == "" })
		if len(parts) > 0 {
			instructions = "<dyna_instructions>\n" + strings.Join(parts, "\n\n") + "\n</dyna_instructions>"
		}
		next.instructions = true
	}
	for i, input := range inputs {
		text := input.Text
		if i == 0 && instructions != "" {
			text = instructions + "\n\n<user_request>\n" + text + "\n</user_request>"
		}
		blocks = append(blocks, map[string]any{"type": "text", "text": text})
	}
	var total int64
	for _, input := range inputs {
		attached, err := attachmentBlocks(input.Files, p, &total)
		if err != nil {
			return nil, nil, err
		}
		blocks = append(blocks, attached...)
	}
	if !slash {
		info := "<runtime_info>In case you're asked: you are running in Dyna through the agy harness, as " + model.ID
		if model.Thinking != "" {
			info += " with " + string(model.Thinking) + " reasoning effort"
		}
		info += ". No need to mention this otherwise.</runtime_info>"
		if info != state.runtime {
			blocks = append(blocks, map[string]any{"type": "text", "text": info})
			next.runtime = info
		}
	}
	commit := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.told.gen != next.gen {
			return
		}
		h.told = next
	}
	return blocks, commit, nil
}

// resetTold makes the next prompt carry everything again: agy's process restarted, or its conversation was compacted or is new
// (T3 adapter.ts compactionMarkers). Harness.mu is held.
func (h *Harness) resetTold() {
	h.told = told{gen: h.told.gen + 1}
}

// begin sends one prompt and, once agy has it, writes the inputs to the mirror and tells the caller. t.open already counts it.
func (h *Harness) begin(t *turn, inputs []contract.TurnInput, started func()) error {
	h.mu.Lock()
	p, sid := h.p, h.sessionID
	h.mu.Unlock()
	blocks, commit, err := h.promptBlocks(inputs, p)
	if err != nil {
		h.mu.Lock()
		t.open--
		t.settle()
		h.mu.Unlock()
		t.signal()
		return err
	}
	// The log is read before the prompt so that only the steps it adds are looked at.
	log, warning := sessiondb.Open(h.cfg.GeminiDir, sid)
	if warning != "" {
		h.notice(warning)
	}
	pending, err := p.client.Start("session/prompt", map[string]any{"sessionId": sid, "prompt": blocks})
	if err != nil {
		h.mu.Lock()
		t.open--
		t.settle()
		h.mu.Unlock()
		t.signal()
		return h.exitError("session/prompt", err, p)
	}
	// agy has the prompt now. A mirror that cannot be written fails the turn when the prompt ends, but does not leave it running unseen.
	if err := h.recordInputs(inputs); err != nil {
		h.mu.Lock()
		t.err = errors.Join(t.err, err)
		h.mu.Unlock()
	}
	t.queuedOnce.Do(func() { close(t.queued) })
	if started != nil {
		started()
	}
	go func() {
		var raw struct {
			StopReason string `json:"stopReason"`
			Usage      struct {
				InputTokens       int64 `json:"inputTokens"`
				OutputTokens      int64 `json:"outputTokens"`
				CachedReadTokens  int64 `json:"cachedReadTokens"`
				CachedWriteTokens int64 `json:"cachedWriteTokens"`
			} `json:"usage"`
		}
		err := pending.Wait(context.Background(), &raw)
		// Unlocked: the log is read from disk.
		var step *sessiondb.Step
		if err == nil && log != nil {
			var warning string
			if step, warning = log.Check(); warning != "" {
				h.notice(warning)
			}
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		t.finishPrompt(h, p, raw.StopReason, step, raw.Usage.InputTokens, raw.Usage.OutputTokens, raw.Usage.CachedReadTokens, raw.Usage.CachedWriteTokens, err)
		// T3 marks the prompt's contents as told unless it was cancelled, refused or failed (adapter.ts:10185-10192).
		if err == nil && raw.StopReason != "cancelled" && raw.StopReason != "refusal" {
			h.mu.Unlock()
			commit()
			h.mu.Lock()
		}
	}()
	return nil
}

// finishPrompt takes a prompt's response. Harness.mu is held.
func (t *turn) finishPrompt(h *Harness, p *process, reason string, step *sessiondb.Step, in, out, cachedRead, cachedWrite int64, err error) {
	t.open--
	t.settle()
	defer t.signal()
	if err != nil {
		if t.aborted || t.err != nil {
			return
		}
		var rpc *acp.Error
		if errors.As(err, &rpc) {
			if t.fail = classify(rpc.Message+"\n"+string(rpc.Data), time.Now(), t.answered); t.fail != nil {
				h.noteBroken(t.fail)
				return
			}
			t.err = fmt.Errorf("agy session/prompt: %w", err)
			return
		}
		t.err = h.exitError("session/prompt", err, p)
		return
	}
	t.usage.InputTokens += in + cachedRead
	t.usage.OutputTokens += out
	t.usage.CacheReadTokens += cachedRead
	t.usage.CacheWriteTokens += cachedWrite
	h.context = in + cachedRead
	if reason == "cancelled" {
		t.stopped = true
		return
	}
	if t.fail == nil {
		if t.fail = outcome(reason, step, time.Now()); t.fail == nil {
			t.fail = stopFailure(reason)
		}
		h.noteBroken(t.fail)
	}
}

func (h *Harness) noteBroken(f *failure) {
	if f != nil && f.broken {
		h.broken = true
	}
}

// watch ends a turn agy has left silent, and a settled turn held too long (quirks 1 and 31). The 15 minutes of silence do
// not count while a tool call is open (T3 Base:3233-3269, 10764-10774).
func (h *Harness) watch(t *turn, stop <-chan struct{}) {
	ticker := time.NewTicker(h.watchEvery)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
		h.mu.Lock()
		silent := t.open > 0 && t.openCalls == 0 && time.Since(t.lastUpdate) > h.promptInactivity
		settled := t.open == 0 && !t.settledAt.IsZero()
		if settled && time.Since(t.settledAt) > h.holdMax {
			t.holdDone = true
		}
		p := h.p
		if silent && t.err == nil {
			t.err = errInactive
		}
		h.mu.Unlock()
		if settled {
			h.checkNative(t)
			t.signal()
		}
		if silent {
			p.kill()
			return
		}
	}
}

// request answers what agy asks. Permissions are answered here as T3 does (adapter.ts:8222-8290: a request with no turn, or a
// question nobody answers, is cancelled) (adapter.ts:1742 selectPermissionOptionId, selectAutoApprovedPermissionOption):
// allow once, else allow always; nothing is rejected. An approval nobody is waiting for, or a question agy puts as a
// permission, is cancelled so agy is not left hanging.
func (h *Harness) request(method string, raw json.RawMessage) (any, error) {
	if method != "session/request_permission" {
		return nil, &acp.Error{Code: acp.MethodNotFound, Message: "method not found: " + method}
	}
	var req struct {
		ToolCall struct {
			ID    string `json:"toolCallId"`
			Title string `json:"title"`
		} `json:"toolCall"`
		Options []struct {
			ID   string `json:"optionId"`
			Kind string `json:"kind"`
		} `json:"options"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	cancelled := map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}
	h.mu.Lock()
	running := h.turn != nil
	h.mu.Unlock()
	if !running {
		h.notice("agy asked permission with no turn running: " + harness.Mask(req.ToolCall.Title))
		return cancelled, nil
	}
	// Choices of a question are not approvals (AntigravityProtocol.ts:78-83).
	if strings.HasPrefix(req.ToolCall.ID, "interaction_") {
		return cancelled, nil
	}
	for _, kind := range []string{"allow_once", "allow_always"} {
		for _, o := range req.Options {
			if o.Kind == kind {
				return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": o.ID}}, nil
			}
		}
	}
	return cancelled, nil
}

// notify folds a session/update into the running turn and the harness's caches.
func (h *Harness) notify(method string, raw json.RawMessage) {
	if method != "session/update" {
		return
	}
	var msg struct {
		SessionID string `json:"sessionId"`
		Update    struct {
			Kind     string          `json:"sessionUpdate"`
			Content  json.RawMessage `json:"content"`
			ID       string          `json:"toolCallId"`
			Name     string          `json:"name"`
			Title    string          `json:"title"`
			ToolKind string          `json:"kind"`
			Status   string          `json:"status"`
			Input    json.RawMessage `json:"rawInput"`
			Output   json.RawMessage `json:"rawOutput"`
			Used     int64           `json:"used"`
			Commands []struct {
				Name string `json:"name"`
			} `json:"availableCommands"`
		} `json:"update"`
	}
	if json.Unmarshal(raw, &msg) != nil {
		return
	}
	u := msg.Update
	h.mu.Lock()
	defer h.mu.Unlock()
	// An update of another session, such as a late one of a conversation agy has left, is not this turn's (T3 Protocol:59-76, quirk 2).
	if u.Kind != "available_commands_update" && h.sessionID != "" && msg.SessionID != h.sessionID {
		return
	}
	switch u.Kind {
	case "usage_update":
		h.used = u.Used
	case "available_commands_update":
		h.commands = map[string]bool{}
		for _, c := range u.Commands {
			h.commands[c.Name] = true
		}
	}
	t := h.turn
	if t == nil {
		return
	}
	t.lastUpdate = time.Now()
	defer t.signal()
	switch u.Kind {
	case "usage_update":
		// A reply that came with no prompt open ends with a usage update (T3 replyEndsWithUsageUpdate).
		if t.open == 0 {
			t.reply = false
		}
	case "agent_message_chunk", "agent_thought_chunk":
		var c struct{ Text string }
		if json.Unmarshal(u.Content, &c) != nil || c.Text == "" {
			return
		}
		kind := "text"
		if u.Kind == "agent_thought_chunk" {
			kind = "thinking"
			t.answered = true
		} else {
			if t.open == 0 {
				t.reply = true
			}
			t.cur += c.Text
			t.last = t.cur
		}
		if n := len(t.blocks); n > 0 && t.blocks[n-1].kind == kind {
			t.blocks[n-1].text += c.Text
		} else {
			t.blocks = append(t.blocks, block{kind: kind, text: c.Text})
		}
	case "tool_call", "tool_call_update":
		h.toolUpdate(t, u.ID, u.Name, u.Title, u.ToolKind, u.Status, u.Input, u.Output, u.Content)
	}
}

type toolContent struct {
	Content struct{ Text string } `json:"content"`
}

// toolUpdate records a tool call and, when it ends, its result. Harness.mu is held.
func (h *Harness) toolUpdate(t *turn, id, name, title, kind, status string, input, output, content json.RawMessage) {
	c := t.calls[id]
	if c == nil {
		if id == "" {
			return
		}
		if name == "" {
			name = title
		}
		if name == "" {
			name = kind
		}
		c = &call{name: name}
		t.answered = true
		// A command that is not a daemon, or a schedule, runs on after the prompt ends (T3 AntigravityProtocol.ts:60-76).
		var in struct{ IsDaemon bool }
		_ = json.Unmarshal(input, &in)
		c.outlives = scheduleTitles[title] || (kind == "execute" && !in.IsDaemon)
		if c.outlives {
			t.outlive++
		}
		t.calls[id] = c
		t.openCalls++
		t.cur = ""
		var args any = map[string]any{}
		if json.Unmarshal(input, &args) != nil {
			args = map[string]any{}
		}
		args = stripSummary(args)
		t.blocks = append(t.blocks, block{kind: "toolCall", call: map[string]any{"type": "toolCall", "id": id, "name": name, "arguments": args}})
		label := name
		if first, _, _ := strings.Cut(title, "\n"); first != "" && first != name {
			label += " " + first
		}
		t.activity(t.id, contract.ActivityTool, label)
	}
	if c.closed || (status != "completed" && status != "failed") {
		return
	}
	c.closed = true
	t.openCalls--
	if c.outlives {
		t.outlive--
		// A finished background command wakes agy, which replies by itself; the reply's usage_update ends it
		// (T3 wakeOnDetachedRequest, replyEndsWithUsageUpdate).
		if t.open == 0 {
			t.reply = true
		}
	}
	t.flush(h)
	var text []string
	var parts []toolContent
	if json.Unmarshal(content, &parts) == nil {
		for _, p := range parts {
			if p.Content.Text != "" {
				text = append(text, p.Content.Text)
			}
		}
	}
	if len(text) == 0 && len(output) > 0 {
		var out struct{ Output string }
		if json.Unmarshal(output, &out) == nil && out.Output != "" {
			text = append(text, out.Output)
		} else {
			text = append(text, string(output))
		}
	}
	failed := status == "failed"
	err := h.record(map[string]any{
		"role": "toolResult", "toolCallId": id, "toolName": c.name, "isError": failed,
		"content": []map[string]any{{"type": "text", "text": strings.Join(text, "\n")}},
	})
	if err != nil && t.err == nil {
		t.err = err
	}
	if t.toolFinish != nil {
		t.toolFinish(t.id, contract.ToolFinished{Call: id, Failed: failed})
	}
}

// flush writes the assistant message built so far to the mirror. Harness.mu is held.
func (t *turn) flush(h *Harness) {
	if len(t.blocks) == 0 {
		return
	}
	content := make([]map[string]any, 0, len(t.blocks))
	for _, b := range t.blocks {
		switch b.kind {
		case "text":
			content = append(content, map[string]any{"type": "text", "text": b.text})
		case "thinking":
			content = append(content, map[string]any{"type": "thinking", "thinking": b.text})
		default:
			content = append(content, b.call)
		}
	}
	t.blocks = nil
	if err := h.record(map[string]any{"role": "assistant", "content": content}); err != nil && t.err == nil {
		t.err = err
	}
}

// Continue reports the turn failed: agy cannot pick up a turn a stopped box agent left, and the engine retries.
func (h *Harness) Continue(_ context.Context, id contract.TurnID, _ func()) (contract.TurnEnded, error) {
	return contract.TurnEnded{Turn: id, Outcome: contract.TurnFailed, Cause: contract.CauseHarnessFailed, Error: "agy session was restarted; the engine retries"}, nil
}

// Recover reports the turn failed too, settled only if the mirror shows the turn's first input was delivered.
func (h *Harness) Recover(id contract.TurnID, inputs []contract.TurnInput) (contract.TurnEnded, bool, error) {
	if len(inputs) == 0 {
		return contract.TurnEnded{Turn: id}, false, errors.New("recovery needs at least one input")
	}
	res := contract.TurnEnded{Turn: id, Outcome: contract.TurnFailed, Cause: contract.CauseHarnessFailed, Error: "agy session was restarted; the engine retries"}
	found, err := h.Delivered(context.Background(), inputs[0].Delivery)
	return res, found, err
}

// Steer is a second prompt while the first runs: agy 1.5 and later steers the turn with it (T3 ACPS:77-85, adapter.ts:10229-10345).
func (h *Harness) Steer(_ context.Context, input contract.TurnInput) error {
	h.mu.Lock()
	t, p := h.turn, h.p
	if t == nil || t.closing {
		h.mu.Unlock()
		return harness.ErrNoTurn
	}
	if !p.steers {
		h.mu.Unlock()
		return fmt.Errorf("agy %s cannot steer a running turn; it needs 1.5.0", p.version)
	}
	t.open++
	t.kept, t.cur = joinReply(t.kept, t.cur), ""
	h.mu.Unlock()
	return h.begin(t, []contract.TurnInput{input}, nil)
}

// FollowUp is held until the prompt answers, then sent as a prompt of its own: agy only steers.
func (h *Harness) FollowUp(_ context.Context, input contract.TurnInput) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.turn == nil || h.turn.closing {
		return harness.ErrNoTurn
	}
	h.held = append(h.held, input)
	return nil
}

// ClearQueue drops the held follow-ups.
func (h *Harness) ClearQueue(context.Context) error {
	h.mu.Lock()
	h.held = nil
	h.mu.Unlock()
	return nil
}

// Abort cancels the running turn.
func (h *Harness) Abort(ctx context.Context) error {
	h.mu.Lock()
	t := h.turn
	h.mu.Unlock()
	if t != nil {
		return h.cancel(ctx, t)
	}
	// A turn that ended with agy still running a command or a reply: Stop cancels it, which a new message never does
	// (T3 Adapter:252-269, ARUN:2653-2657, quirk 3).
	h.mu.Lock()
	p, sid, detached := h.p, h.sessionID, h.detached
	h.detached = false
	h.mu.Unlock()
	if detached && p != nil {
		return p.client.Notify("session/cancel", map[string]any{"sessionId": sid})
	}
	return nil
}

// cancel is T3's cancel (ARUN:2625-2688, cancelBehavior "wait-for-prompt"): agy drops a cancel that reaches it before its
// prompt, so wait until the prompt is written; send session/cancel; wait for the prompt's response; kill the process if
// it does not come. Each wait has its own CancelTimeout.
func (h *Harness) cancel(ctx context.Context, t *turn) error {
	h.mu.Lock()
	p, sid := h.p, h.sessionID
	t.aborted = true
	t.settle()
	h.held = nil
	h.mu.Unlock()
	t.signal() // a turn held after its response ends now
	if p == nil {
		return nil
	}
	wait := time.NewTimer(h.cfg.CancelTimeout)
	select {
	case <-t.queued:
	case <-t.finished:
		wait.Stop()
		return nil
	case <-wait.C:
	case <-ctx.Done():
		wait.Stop()
		return ctx.Err()
	}
	wait.Stop()
	if err := p.client.Notify("session/cancel", map[string]any{"sessionId": sid}); err != nil {
		p.kill()
		return nil
	}
	wait = time.NewTimer(h.cfg.CancelTimeout)
	defer wait.Stop()
	select {
	case <-t.idle:
	case <-wait.C:
		p.kill()
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

// settle closes idle once an aborted turn has no prompt open. Harness.mu is held.
func (t *turn) settle() {
	if t.aborted && t.open == 0 {
		t.idleOnce.Do(func() { close(t.idle) })
	}
}

// signal wakes Start to look at the turn again.
func (t *turn) signal() {
	select {
	case t.wake <- struct{}{}:
	default:
	}
}

// SetPromptSections keeps the sections: a later change reaches agy as a block at the head of the next prompt.
func (h *Harness) SetPromptSections(_ context.Context, sections []contract.ContextSection) error {
	h.mu.Lock()
	h.sections = slices.Clone(sections)
	h.mu.Unlock()
	return nil
}

// TransferModel sets the model and thought level with session/set_config_option. A model agy does not list is refused.
func (h *Harness) TransferModel(ctx context.Context, id contract.TurnID, model contract.ModelChoice, _ int, instructions string) (harness.ModelTransfer, error) {
	if err := h.ensure(ctx); err != nil {
		return harness.ModelTransfer{}, err
	}
	h.mu.Lock()
	previous, p := h.cfg.Model, h.p
	h.cfg.Model = model
	h.mu.Unlock()
	if h.Activity != nil {
		h.Activity(id, contract.ActivityModel, "changing model")
	}
	if err := h.applyModel(ctx, p); err != nil {
		h.mu.Lock()
		h.cfg.Model = previous
		h.mu.Unlock()
		// The model may have changed on agy before the thought level failed: put the previous one back, so agy and Dyna agree.
		_ = h.applyModel(ctx, p)
		return harness.ModelTransfer{}, err
	}
	h.mu.Lock()
	h.modelText = instructions
	h.mu.Unlock()
	path := "switched"
	if previous == model {
		path = "unchanged"
	}
	return harness.ModelTransfer{Path: path}, nil
}

// Compact sends /compact as a prompt, only if agy lists the command and the last prompt's context was over the size.
func (h *Harness) Compact(ctx context.Context, over int64) (harness.Compaction, error) {
	h.mu.Lock()
	before, listed := h.context, h.commands["compact"]
	h.mu.Unlock()
	if !listed || before <= over {
		return harness.Compaction{}, nil
	}
	if err := h.ensure(ctx); err != nil {
		return harness.Compaction{}, err
	}
	h.mu.Lock()
	p, sid := h.p, h.sessionID
	h.used = 0 // the usage_update after the compaction is what it left
	h.mu.Unlock()
	var res struct {
		StopReason string `json:"stopReason"`
		Usage      struct {
			InputTokens       int64 `json:"inputTokens"`
			OutputTokens      int64 `json:"outputTokens"`
			CachedReadTokens  int64 `json:"cachedReadTokens"`
			CachedWriteTokens int64 `json:"cachedWriteTokens"`
		} `json:"usage"`
	}
	if err := p.client.Call(ctx, "session/prompt", map[string]any{"sessionId": sid, "prompt": []map[string]any{{"type": "text", "text": "/compact"}}}, &res); err != nil {
		return harness.Compaction{}, h.exitError("compact", err, p)
	}
	if res.StopReason != "end_turn" {
		return harness.Compaction{}, fmt.Errorf("agy /compact ended with %q", res.StopReason)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	// What agy was told is gone from its context, so the next prompt carries it again (T3 compactionMarkers).
	h.resetTold()
	u := res.Usage
	return harness.Compaction{
		Compacted: true, TokensBefore: before, TokensAfter: h.used,
		Usage: contract.Usage{InputTokens: u.InputTokens + u.CachedReadTokens, OutputTokens: u.OutputTokens, CacheReadTokens: u.CachedReadTokens, CacheWriteTokens: u.CachedWriteTokens},
	}, nil
}

// NewConversation opens a new agy session and a new mirror file. An empty conversation stays as it is.
func (h *Harness) NewConversation(ctx context.Context) error {
	if err := h.ensure(ctx); err != nil {
		return err
	}
	h.mu.Lock()
	p := h.p
	h.mu.Unlock()
	path := h.mirrorPath()
	if info, err := os.Stat(path); err != nil || info.Size() == 0 {
		return nil
	}
	return h.newSession(ctx, p)
}

// NewConversationForce opens a new session even when the conversation has entries.
func (h *Harness) NewConversationForce(ctx context.Context) error {
	if err := h.ensure(ctx); err != nil {
		return err
	}
	h.mu.Lock()
	p := h.p
	h.mu.Unlock()
	return h.newSession(ctx, p)
}

func (h *Harness) newSession(ctx context.Context, p *process) error {
	if err := h.openSession(ctx, p, ""); err != nil {
		return err
	}
	h.delivered.mu.Lock()
	h.delivered.path, h.delivered.read, h.delivered.seen = "", 0, nil
	h.delivered.mu.Unlock()
	h.mu.Lock()
	h.context, h.used, h.broken = 0, 0, false
	h.resetTold()
	h.mu.Unlock()
	return nil
}

var scheduleTitles = map[string]bool{"Run schedule": true, "Running schedule": true}

// stripSummary removes the toolSummary agy adds to MCP call arguments, at any depth (T3 Protocol:419-465, quirk 11).
func stripSummary(v any) any {
	switch v := v.(type) {
	case map[string]any:
		delete(v, "toolSummary")
		for k, e := range v {
			v[k] = stripSummary(e)
		}
	case []any:
		for i, e := range v {
			v[i] = stripSummary(e)
		}
	}
	return v
}
