package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
	"github.com/resident-labs/dyna/engine/transcriptview"
)

// advisorTimeout is how long root waits for the advisor's answer. It is a variable so that a test need not wait it out.
var advisorTimeout = 15 * time.Minute

// Advisor posts root's prompt to the room's advisor and waits for the end of the turn that takes it, whatever its outcome. The answer
// is the turn's final text, which the engine also posts to the feed from the advisor. A wait that ends early, by timeout or by root's
// stop, leaves the advisor's reply to arrive as an input, as a child's result does.
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
	advisor, err := s.store.RoomAdvisor(ctx, self.Room)
	if errors.Is(err, store.ErrNotFound) {
		return contract.AdvisorResponse{}, invalid("this room has no advisor; set one with the room's advisor setting (root's About panel, or `dyna room advisor ROOM PROFILE`)")
	}
	if err != nil {
		return contract.AdvisorResponse{}, failure(err)
	}
	since, err := s.store.LastTurnEnd(ctx, advisor.ID)
	if err != nil {
		return contract.AdvisorResponse{}, failure(err)
	}
	delta, problem := s.advisorDelta(ctx, self.ID, since)
	if problem != nil {
		return contract.AdvisorResponse{}, problem
	}
	// Subscribe before the post, so that an end cannot fall between the post and the first look.
	changes, unsubscribe := s.store.Subscribe()
	defer func() { unsubscribe() }()
	post, problem := s.Post(ctx, caller, contract.PostRequest{Room: self.Room, Text: req.Prompt, Context: delta, Mentions: []string{advisor.Name}, Awaited: true})
	if problem != nil {
		return contract.AdvisorResponse{}, problem
	}
	waitCtx, cancel := context.WithTimeout(ctx, advisorTimeout)
	defer cancel()
	for {
		ended, done, err := s.store.DeliveryTurnEnded(waitCtx, advisor.ID, post.Seq)
		if err != nil && waitCtx.Err() == nil {
			return contract.AdvisorResponse{}, failure(err)
		}
		if done {
			return s.advisorAnswer(ctx, self.Room, advisor.ID, ended)
		}
		select {
		case <-waitCtx.Done():
			// The reply is owed from here on, unless the turn ended just now.
			settle := context.WithoutCancel(ctx)
			owed, err := s.store.OweReply(settle, advisor.ID, post.Seq, self.ID)
			if err != nil {
				return contract.AdvisorResponse{}, failure(err)
			}
			if !owed {
				if ended, done, err := s.store.DeliveryTurnEnded(settle, advisor.ID, post.Seq); err == nil && done {
					return s.advisorAnswer(settle, self.Room, advisor.ID, ended)
				}
			}
			if ctx.Err() != nil {
				return contract.AdvisorResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: "the advisor call was cancelled; its reply will still arrive as an input"}
			}
			return contract.AdvisorResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: fmt.Sprintf("the advisor did not answer within %s; its reply will still arrive as an input", advisorTimeout)}
		case _, ok := <-changes:
			if !ok {
				unsubscribe()
				changes, unsubscribe = s.store.Subscribe()
			}
		}
	}
}

// advisorAnswer shows the advisor's final text in the feed and returns it as root's result.
func (s *Service) advisorAnswer(ctx context.Context, room contract.RoomID, advisor contract.SessionID, ended contract.TurnEnded) (contract.AdvisorResponse, *contract.Error) {
	answer := ended.Final
	if answer == "" {
		answer = "The advisor's turn ended (" + string(ended.Outcome) + ") with no text. " + ended.Error
	}
	_, problem := s.Post(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: advisor}, contract.PostRequest{Room: room, Text: answer, Home: contract.Conversation{Kind: contract.ConversationFeed}})
	if problem != nil {
		return contract.AdvisorResponse{}, problem
	}
	return contract.AdvisorResponse{Answer: answer}, nil
}

// advisorName is the name of the session the room's advisor setting starts.
const advisorName = "advisor"

// setAdvisor makes the room's advisor match the setting: with a profile, one session beside root on it, started if missing and
// switched when its profile differs; with none, no session. The setting is that session, so nothing else stores it.
func (s *Service) setAdvisor(ctx context.Context, caller contract.Principal, room contract.RoomID, profile contract.ProfileName) *contract.Error {
	if profile != "" {
		if _, _, err := s.profiles.Model(profile); err != nil {
			return invalid("unknown profile " + string(profile))
		}
	}
	advisor, err := s.store.RoomAdvisor(ctx, room)
	missing := errors.Is(err, store.ErrNotFound)
	if err != nil && !missing {
		return failure(err)
	}
	switch {
	case missing && profile == "":
		return nil
	case missing:
		_, problem := s.startSession(ctx, caller, contract.SessionStartRequest{Room: room, Name: advisorName, Profile: profile, Advisor: true}, "", nil)
		return problem
	case profile == "":
		_, problem := s.SessionClose(ctx, caller, contract.SessionCloseRequest{Session: advisor.ID})
		return problem
	case advisor.Profile != profile:
		_, problem := s.SessionSetProfile(ctx, caller, contract.SessionSetProfileRequest{Session: advisor.ID, Profile: profile})
		return problem
	}
	return nil
}

const (
	advisorResultChars = 2000 // a longer tool result is cut, and transcript read has the rest
	advisorFirstSteps  = 50   // the first call carries root's current turn, or this many of its last steps
)

// advisorDelta renders root's transcript for the advisor: root's inputs, its text and each tool call with its arguments and result,
// since the advisor last answered. With no earlier answer it is root's current turn, or the last advisorFirstSteps steps of it.
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
				lines = append(lines, fmt.Sprintf("input from %s%s: %s", input.From.Kind, input.From.Session, input.Text))
			}
		}
		for _, item := range turn.Items {
			if !item.At.After(since) {
				continue
			}
			switch item.Kind {
			case contract.TranscriptText:
				lines = append(lines, "root: "+item.Text)
			case contract.TranscriptTool:
				line := fmt.Sprintf("tool %s %s", item.Tool, transcriptview.Clip(item.Arguments, advisorResultChars))
				if item.Failed {
					line += "\nfailed:"
				}
				line += "\nresult: " + transcriptview.Clip(item.Output, advisorResultChars)
				if item.More || len(item.Output) > advisorResultChars {
					line += "\n(cut; transcript read has the rest)"
				}
				lines = append(lines, line)
			}
		}
		out = append(out, lines...)
	}
	if since.IsZero() {
		out = out[max(0, len(out)-advisorFirstSteps):]
	}
	if len(out) == 0 {
		return "Root's work since your last answer: nothing new.", nil
	}
	return "Root's work since your last answer:\n\n" + strings.Join(out, "\n\n"), nil
}

// IsAdvisor reports whether a principal is the advisor session.
func (s *Service) IsAdvisor(ctx context.Context, who contract.Principal) bool {
	if who.Kind != contract.PrincipalSession {
		return false
	}
	session, err := s.store.Session(ctx, who.Session)
	return err == nil && session.Advisor
}
