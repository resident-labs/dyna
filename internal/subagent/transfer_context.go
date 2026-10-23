package subagent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"dyna-agent/internal/childenv"
	"dyna-agent/internal/goals"
	"dyna-agent/internal/handoff"
	"dyna-agent/internal/pr/links"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/workeroutput"
)

// scanTail bounds both disk input and the size of a single decoded record.
func scanTail(path string, after int64, visit func([]byte)) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return
	}
	start := max(after, st.Size()-256*1024)
	if start < 0 {
		start = 0
	}
	if _, err = f.Seek(start, io.SeekStart); err != nil {
		return
	}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 256*1024)
	if start > after {
		scanner.Scan()
	} // a partial first JSON record is not evidence
	for scanner.Scan() {
		visit(scanner.Bytes())
	}
}

// truncationNote marks clipped text and names where the rest lives. Its length
// is reserved when a handoff is clipped, so the bound covers the note too.
const truncationNote = "\n[truncated; use the recovery commands for the complete text]"

// cut bounds text at a rune boundary, without a note.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && (s[n]&0xc0) == 0x80 {
		n--
	}
	return s[:n]
}

// clipped bounds text and says where the complete text lives.
func clipped(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return cut(s, n) + truncationNote
}

// shownAddressLimit is how much of a session address the packet header and the
// fallback notice display. Both show the same address, so they share one bound;
// the session ID stays the reliable handle beyond it.
const shownAddressLimit = 120

// shownAddress bounds a session address that the packet or a notice displays.
// A session name may be very long, and the session ID stays the reliable
// handle, so the header never spends the packet's budget on a name.
func shownAddress(addr string) string {
	if len(addr) <= shownAddressLimit {
		return addr
	}
	return cut(addr, shownAddressLimit) + "…"
}

// handoffPacketLimit is the most a replacement model receives. The packet
// names work and commands; it never copies transcripts or room posts.
const handoffPacketLimit = 12 * 1024

// packetHandoffLimit bounds an inlined written handoff. The complete text
// stays in the saved transfer record.
const packetHandoffLimit = 6000

// rootSeatHandoffLimit bounds a root seat handoff, which is the document that
// replaces a room's root rather than input to a replacement model. It matches
// the largest handoff a session can submit.
const rootSeatHandoffLimit = 32 * 1024

// packetHeader names who continues the work and why it moved.
func (m *Manager) packetHeader(meta Meta, t TransferState) string {
	var b strings.Builder
	address := shownAddress(meta.Address())
	if t.RootSeat {
		fmt.Fprintf(&b, "[DYNA ROOT SEAT HANDOFF]\nContinue this room as its external root. Preserve the internal root's work.\nSession: %s (%s)\nLaunch directory (can be stale): %s\nReason: %s\n", address, meta.ID, meta.Dir, clipped(t.Reason, 300))
		return b.String()
	}
	fmt.Fprintf(&b, "[DYNA HANDOFF PACKET]\nContinue this Dyna session as the replacement model. You have no native context from the prior model. Preserve its work. The session, room, address, parent, children, questions, goals and PR roles stay the same.\nSession: %s (%s)\nSwitch: %s\nProfile/model: %s / %s -> %s / %s\n", address, meta.ID, clipped(t.Reason, 300), t.PreviousProfile, orNone(t.PreviousModel), t.Profile, orNone(t.Model))
	if t.Limit != "" {
		fmt.Fprintf(&b, "Moved because: %s\n", clipped(t.Limit, 200))
	}
	return b.String()
}

// writeOpenWork lists the session's live responsibilities: goals with the
// keeper's latest journal line and open items, the PRs it authors or reviews,
// open children and open questions. Each section and line is bounded.
func (m *Manager) writeOpenWork(b *strings.Builder, meta Meta, tracked []links.Link) {
	if held, err := m.goalStore().OpenHeldBy(meta.RoomID, meta.ID); err == nil && len(held) > 0 {
		b.WriteString("\nGoals:\n")
		progress := m.journalProgress(meta)
		for i, g := range held {
			if i == 6 {
				fmt.Fprintf(b, "  (more goals: `dyna goal --for %s`)\n", meta.Address())
				break
			}
			fmt.Fprintf(b, "Goal %s: %s\n", g.ID, clipped(g.Text, 600))
			if p := progress[g.ID]; p != "" {
				fmt.Fprintf(b, "  Latest progress: %s\n", clipped(p, 400))
			}
			if items, err := m.goalStore().Items(meta.RoomID, g.ID); err == nil {
				open := 0
				for _, item := range items {
					if item.State != goals.ItemOpen {
						continue
					}
					if open == 4 {
						fmt.Fprintf(b, "  (more items: `dyna goal show %s --json`)\n", g.ID)
						break
					}
					open++
					fmt.Fprintf(b, "  Item %s: %s %s\n", item.ID, clipped(item.Text, 200), item.Assignee)
				}
			}
		}
	}
	var prs strings.Builder
	for i, link := range tracked {
		if i == 12 {
			prs.WriteString("  (more PRs: `dyna git pr list`)\n")
			break
		}
		fmt.Fprintf(&prs, "  PR #%d %s %s roles: %s head: %s\n", link.Number, link.State, link.URL, strings.Join(link.Roles, ", "), orNone(link.Head))
	}
	if prs.Len() > 0 {
		b.WriteString("\nPRs this session authors or reviews:\n")
		b.WriteString(prs.String())
	}
	if children, err := m.OpenChildren(meta.ID); err == nil {
		var out strings.Builder
		open := 0
		for _, child := range children {
			if child.Status == StatusClosed || child.Status == StatusDormant {
				continue
			}
			if open == 10 {
				break
			}
			open++
			fmt.Fprintf(&out, "  Child %s (%s): %s\n", child.ID, child.Status, clipped(child.Name, 120))
		}
		if out.Len() > 0 {
			b.WriteString("\nOpen children:\n")
			b.WriteString(out.String())
		}
	}
	if qs, err := m.questionStore().OpenAskedBy(meta.RoomID, meta.ID); err == nil && len(qs) > 0 {
		b.WriteString("\nOpen questions:\n")
		for i, q := range qs {
			if i == 10 {
				break
			}
			fmt.Fprintf(b, "  Question %s (to %s): %s\n", q.ID, shownAddress(q.To), clipped(q.Text, 300))
		}
	}
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(none)"
	}
	return s
}

// journalProgress maps each goal to the keeper's newest journal line about
// this session, read from the room's last 20 check-ins.
func (m *Manager) journalProgress(meta Meta) map[string]string {
	out := map[string]string{}
	entries, _, err := m.goalStore().Journal(meta.RoomID, "", 20)
	if err != nil {
		return out
	}
	for _, e := range entries {
		for _, l := range e.Entries {
			for _, id := range l.Goals {
				if l.Agent == meta.Address() && out[id] == "" {
					out[id] = strings.ReplaceAll(l.Status, "_", " ") + ": " + l.Summary
				}
			}
		}
	}
	return out
}

func (m *Manager) transferHasOutput(id string, t Turn) bool {
	path, _ := m.store().NormalizedPath(id)
	found := false
	scanTail(path, t.NormStart, func(line []byte) {
		var e workeroutput.Event
		// A started step means the model is running. OpenCode prints no
		// reasoning, so a long first thought shows only its step_start.
		if json.Unmarshal(line, &e) == nil && ((e.Kind == "message" && e.Role == "assistant" && strings.TrimSpace(e.Text) != "") || e.Kind == "tool" || e.Kind == "reasoning" ||
			(e.Kind == "status" && e.Name == "step" && e.State == "started")) {
			found = true
		}
	})
	return found
}

func validTaskDir(dir string) bool { st, err := os.Stat(dir); return err == nil && st.IsDir() }

// sourceReview is the shared session summary and review pointer. Full evidence
// stays in transfer state, turns and room records, rather than replacement input.
func (m *Manager) sourceReview(meta Meta, turn int) string {
	summary := ""
	if held, err := m.goalStore().OpenHeldBy(meta.RoomID, meta.ID); err == nil && len(held) > 0 {
		summary = fmt.Sprintf("%d open goals; current task: %s", len(held), held[0].Text)
	}
	if summary == "" {
		first, _ := m.store().ReadTurn(meta.ID, 1)
		summary = first.Prompt
	}
	return handoff.Review(meta.ID, summary,
		fmt.Sprintf("`dyna subagent output %s --turn 1 --transcript` (the original brief)", meta.ID),
		fmt.Sprintf("`dyna subagent output %s --turn %d --transcript` (any turn's transcript)", meta.ID, max(1, turn)),
		fmt.Sprintf("`dyna subagent history %s --search '<pattern>'`", meta.ID),
		fmt.Sprintf("`dyna subagent show %s --json`", meta.ID),
		fmt.Sprintf("`dyna subagent transfer status %s --json` (the saved handoff)", meta.ID),
		fmt.Sprintf("`dyna room log %s` (recent posts to and from it)", meta.RoomID))
}

// handoffPacket is the one context packet for a replacement model: transfer,
// consolidation and usage-limit fallback all use it. It names live work and
// how to read the old conversation, and it inlines no transcript excerpts or
// room posts. It also resolves the replacement directory from saved hints.
func (m *Manager) handoffPacket(meta Meta, t TransferState) (string, string, error) {
	dir := meta.Dir
	hintDir, hintBranch := handoffHints(t.Handoff)
	if t.Dir == "" && validTaskDir(hintDir) {
		t.Dir = hintDir
	}
	if t.Branch == "" {
		t.Branch = hintBranch
	}
	tracked, err := links.ForSession(state.Root(filepath.Dir(m.store().root())), meta.ID, meta.RoomID)
	if err != nil {
		return "", "", err
	}
	for _, link := range tracked {
		if t.Dir == "" && link.Active() {
			for _, role := range link.Roles {
				if role == "author" {
					if validTaskDir(link.LookupDir) {
						dir = link.LookupDir
					} else if validTaskDir(link.Dir) {
						dir = link.Dir
					}
				}
			}
		}
	}
	if t.Dir != "" {
		dir = t.Dir
	}
	if !validTaskDir(dir) {
		return "", "", fmt.Errorf("task directory %q is unavailable; submit a handoff with the actual worktree or restore that directory and retry", dir)
	}
	branch := t.Branch
	if branch == "" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if raw, err := childenv.Clean(exec.CommandContext(ctx, "/usr/bin/git", "-C", dir, "symbolic-ref", "--quiet", "--short", "HEAD")).Output(); err == nil {
			branch = strings.TrimSpace(string(raw))
		}
	}

	var head strings.Builder
	head.WriteString(m.packetHeader(meta, t))
	head.WriteString(m.consolidationManifest(meta))
	m.writeOpenWork(&head, meta, tracked)
	written := ""
	if t.Source == "cooperative" {
		written = t.WrittenHandoff
		if written == "" {
			written = t.Handoff
		}
	}
	if written == "" && !t.RootSeat {
		// The absence note lives with the live work, before the bound check,
		// so it is part of what the bound covers.
		head.WriteString("The previous model left no written handoff, so read its work before changing files.\n")
	}
	var tail strings.Builder
	fmt.Fprintf(&tail, "\nWorktree: %s\nBranch: %s\nCheck `git status` and `git branch --show-current` there before editing.\n", dir, orNone(branch))
	fmt.Fprintf(&tail, "\n%s", m.sourceReview(meta, meta.Turns))
	// Keep the read commands inside the bound: a written handoff takes only
	// the room that is left after the live work and the commands.
	const handoffHeader = "\nWritten handoff:\n"
	if written != "" {
		if t.RootSeat {
			// The root seat document replaces this room's root and is not model
			// input, so its own handoff cap bounds it instead of the packet bound.
			head.WriteString(handoffHeader)
			head.WriteString(clipped(written, rootSeatHandoffLimit))
			head.WriteString("\n")
		} else {
			// The allowance also covers the header and the truncation note, so a
			// clipped handoff still lands inside the bound.
			allowance := min(packetHandoffLimit, handoffPacketLimit-len(head.String())-len(tail.String())-len(handoffHeader)-len(truncationNote))
			if allowance > 0 {
				head.WriteString(handoffHeader)
				head.WriteString(clipped(written, allowance))
				head.WriteString("\n")
			}
		}
	}
	if !t.RootSeat && head.Len()+tail.Len() > handoffPacketLimit {
		// Only a pathological set of goals and PRs reaches this; drop the live
		// work rather than the commands that read the old conversation.
		return tail.String(), dir, nil
	}
	return head.String() + tail.String(), dir, nil
}

// Cooperative final answers can use these labels when no handoff command ran.
func handoffHints(text string) (dir, branch string) {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.ReplaceAll(line, "**", ""))
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), "`\"")
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "worktree", "task worktree", "task directory", "actual task directory", "working directory":
			if filepath.IsAbs(value) {
				dir = value
			}
		case "branch", "task branch":
			if len(value) <= 256 && !strings.ContainsAny(value, "\r\n\x00") {
				branch = value
			}
		}
	}
	return
}
