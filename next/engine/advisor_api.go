package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
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
		return contract.AdvisorResponse{}, invalid("this room has no advisor; session start with advisor: true starts one on a stronger profile")
	}
	if err != nil {
		return contract.AdvisorResponse{}, failure(err)
	}
	// Subscribe before the post, so that an end cannot fall between the post and the first look.
	changes, unsubscribe := s.store.Subscribe()
	defer func() { unsubscribe() }()
	post, problem := s.Post(ctx, caller, contract.PostRequest{Room: self.Room, Text: req.Prompt, Mentions: []string{advisor.Name}, Awaited: true})
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
