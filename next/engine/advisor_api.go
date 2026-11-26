package engine

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/prompt"
	"github.com/resident-labs/dyna/engine/transcriptview"
)

// advisorName names the room's advisor: an in-box agent in root's box, whose conversation persists under this name.
const advisorName = "advisor"

// Advisor asks the room's advisor, an agent that runs in root's box with read-only tools, and waits for its answer. Root's work since
// its last consultation goes ahead of its prompt. A stop, a timeout and the end of root's own turn each return what the advisor had said.
func (s *Service) Advisor(ctx context.Context, caller contract.Principal, req contract.AdvisorRequest) (contract.AdvisorResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalSession {
		return contract.AdvisorResponse{}, forbidden()
	}
	self, problem := s.callerSession(ctx, caller)
	if problem != nil {
		return contract.AdvisorResponse{}, problem
	}
	if !self.Root {
		return contract.AdvisorResponse{}, invalid("only root calls the advisor")
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return contract.AdvisorResponse{}, invalid("prompt is required: the task, what you found so far and what you want judged")
	}
	name, err := s.store.RoomAdvisorProfile(ctx, self.Room)
	if err != nil {
		return contract.AdvisorResponse{}, failure(err)
	}
	if name == "" {
		return contract.AdvisorResponse{}, invalid("this room has no advisor; set one with the room's advisor setting (root's About panel, or `dyna room advisor ROOM PROFILE`)")
	}
	profile, err := s.workflowProfile(name)
	if err != nil {
		return contract.AdvisorResponse{}, invalid("unknown profile " + string(name))
	}
	since, err := s.store.LastAgentEnd(ctx, self.ID, advisorName)
	if err != nil {
		return contract.AdvisorResponse{}, failure(err)
	}
	delta, problem := s.advisorDelta(ctx, self.ID, since)
	if problem != nil {
		return contract.AdvisorResponse{}, problem
	}
	// A conversation the box lost or never had begins with the first call's record, which the box knows only when it opens it.
	spec := agentSpec{ID: contract.AgentID(req.ToolCallID), Name: advisorName, Profile: profile, Instructions: prompt.AdvisorInstructions, Persist: true, Orient: true, Prompt: delta + "\n\n" + req.Prompt}
	if !since.IsZero() {
		first, problem := s.advisorDelta(ctx, self.ID, time.Time{})
		if problem != nil {
			return contract.AdvisorResponse{}, problem
		}
		spec.FreshPrompt = first + "\n\n" + req.Prompt
	}
	if spec.ID == "" {
		generated, err := id("agent_", s.now())
		if err != nil {
			return contract.AdvisorResponse{}, failure(err)
		}
		spec.ID = contract.AgentID(generated)
	}
	result, err := s.askAgent(ctx, self, spec)
	if err != nil {
		return contract.AdvisorResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: "the advisor could not run: " + err.Error()}
	}
	answer := advisorAnswer(result)
	answer.Advisor = name
	return answer, nil
}

// advisorAnswer is root's tool result: the advisor's final text, or what it had when something ended its turn, with a note of what ended it.
func advisorAnswer(result agentResult) contract.AdvisorResponse {
	var reason string
	switch result.Cause {
	case agentCancelled:
		reason = "root was stopped"
	case agentTimedOut:
		reason = fmt.Sprintf("the advisor timed out after %s", agentTimeout)
	case agentBoxGone:
		reason = "the box went away while the advisor ran"
	case "":
		switch {
		case result.Outcome != contract.TurnCompleted:
			reason = "the advisor's turn ended (" + string(result.Outcome) + ")"
			if result.Error != "" {
				reason += ": " + result.Error
			}
		case result.Final == "":
			reason = "the advisor's turn ended with no text"
		default:
			return contract.AdvisorResponse{Answer: result.Final}
		}
	}
	if result.Final == "" {
		return contract.AdvisorResponse{Note: reason + "; it had said nothing yet"}
	}
	return contract.AdvisorResponse{Answer: result.Final, Note: reason + "; the answer is what it had said so far"}
}

// setAdvisor sets the room's advisor: a profile, or none.
func (s *Service) setAdvisor(ctx context.Context, room contract.RoomID, profile contract.ProfileName) *contract.Error {
	if profile != "" {
		if _, _, err := s.profiles.Model(profile); err != nil {
			return invalid("unknown profile " + string(profile))
		}
		if problem := s.pigOnly(profile, "the advisor"); problem != nil {
			return problem
		}
	}
	if err := s.store.SetRoomAdvisorProfile(ctx, room, profile); err != nil {
		return failure(err)
	}
	return nil
}

const (
	advisorTextChars   = 2000  // an input or root's own text is clipped to this
	advisorArgChars    = 200   // a tool call's arguments
	advisorResultChars = 300   // a tool call's result
	advisorFirstSteps  = 10    // the first call carries root's last steps
	advisorMaxSteps    = 30    // every call is cut to the newest steps
	advisorMaxChars    = 24000 // and to about this many characters
)

// advisorDelta renders root's transcript for the advisor as a short record since root's last consultation: its inputs, its text and one line
// per tool call, the newest advisorMaxSteps of them within about advisorMaxChars. The first call has only the last advisorFirstSteps.
func (s *Service) advisorDelta(ctx context.Context, root contract.SessionID, since time.Time) (string, *contract.Error) {
	var turns []contract.TranscriptTurn // newest first, whole
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	for before := contract.TurnID(""); ; {
		read, problem := s.TranscriptRead(ctx, owner, contract.TranscriptReadRequest{Session: root, Before: before, Limit: 5})
		if problem != nil {
			return "", problem
		}
		done := !read.More
		for _, turn := range read.Turns {
			if turn.Ended != nil && turn.Ended.Before(since) || since.IsZero() && len(turns) > 0 {
				done = true
				break
			}
			// A long turn comes back as its newest items; the rest is read from its start in pages.
			for cursor := 0; turn.OlderItems; {
				page, problem := s.TranscriptRead(ctx, owner, contract.TranscriptReadRequest{Session: root, Turn: turn.ID, Page: contract.TranscriptPageOlderItems, Cursor: cursor})
				if problem != nil || len(page.Turns) == 0 {
					return "", problem
				}
				if cursor == 0 {
					turn.Items = nil
				}
				turn.Items = append(turn.Items, page.Turns[0].Items...)
				turn.OlderItems, cursor = page.Turns[0].OlderItems, page.Turns[0].NextItem
			}
			turns = append(turns, turn)
		}
		if done || read.Before == "" {
			break
		}
		before = read.Before
	}
	slices.Reverse(turns)
	var out []string
	for _, turn := range turns {
		if turn.Ended != nil && turn.Ended.Before(since) {
			continue
		}
		var lines []string
		for _, input := range turn.Inputs {
			if input.At.After(since) {
				lines = append(lines, fmt.Sprintf("input from %s%s: %s", input.From.Kind, input.From.Session, transcriptview.Clip(input.Text, advisorTextChars)))
			}
		}
		for _, item := range turn.Items {
			if !item.At.After(since) {
				continue
			}
			switch item.Kind {
			case contract.TranscriptText:
				lines = append(lines, "root: "+transcriptview.Clip(item.Text, advisorTextChars))
			case contract.TranscriptTool:
				line := fmt.Sprintf("tool %s %s -> ", item.Tool, strings.ReplaceAll(transcriptview.Clip(item.Arguments, advisorArgChars), "\n", " "))
				if item.Failed {
					line += "failed: "
				}
				lines = append(lines, line+strings.ReplaceAll(transcriptview.Clip(item.Output, advisorResultChars), "\n", " "))
			}
		}
		out = append(out, lines...)
	}
	steps := advisorMaxSteps
	if since.IsZero() {
		steps = advisorFirstSteps
	}
	kept, size := 0, 0
	for kept < min(steps, len(out)) && size+len(out[len(out)-1-kept]) <= advisorMaxChars {
		size += len(out[len(out)-1-kept])
		kept++
	}
	if kept == 0 {
		return "Root's work since your last answer: nothing new.", nil
	}
	note := ""
	if omitted := len(out) - kept; omitted > 0 {
		note = fmt.Sprintf("%d earlier steps omitted; transcript read has them\n\n", omitted)
	}
	return "Root's work since your last answer:\n\n" + note + strings.Join(out[len(out)-kept:], "\n\n"), nil
}
