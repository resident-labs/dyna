package engine

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/room"
)

// maxQuestionChars keeps a question readable on a Needs you row.
const maxQuestionChars = 4000

// QuestionAsk records a question for the owner. It waits until the owner answers or the session closes, without expiry.
func (s *Service) QuestionAsk(ctx context.Context, caller contract.Principal, req contract.QuestionAskRequest) (contract.QuestionAskResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalSession {
		return contract.QuestionAskResponse{}, forbidden()
	}
	if len(req.ClientID) > 64 {
		return contract.QuestionAskResponse{}, invalid("clientId is longer than 64 characters")
	}
	text := strings.TrimSpace(req.Text)
	if text == "" || utf8.RuneCountInString(text) > maxQuestionChars {
		return contract.QuestionAskResponse{}, invalid("a question is 1 to 4000 characters")
	}
	meanwhile := strings.TrimSpace(req.Meanwhile)
	if meanwhile == "" {
		return contract.QuestionAskResponse{}, invalid("meanwhile is required: say what useful safe work you will do while you wait, or with blocked, why there is none")
	}
	if utf8.RuneCountInString(meanwhile) > maxQuestionChars {
		return contract.QuestionAskResponse{}, invalid("meanwhile is at most 4000 characters")
	}
	session, err := s.store.Session(ctx, caller.Session)
	if err != nil {
		return contract.QuestionAskResponse{}, failure(err)
	}
	now := s.now()
	value, err := id("q_", now)
	if err != nil {
		return contract.QuestionAskResponse{}, failure(err)
	}
	question := contract.Question{ID: contract.QuestionID(value), Room: session.Room, Session: session.ID, Text: text, Meanwhile: meanwhile, Blocked: req.Blocked, State: contract.QuestionStatePending, Asked: now}
	event, err := s.event(session.Room, caller, contract.QuestionAsked{ID: question.ID, Session: session.ID, Text: text, Meanwhile: meanwhile, Blocked: req.Blocked})
	if err != nil {
		return contract.QuestionAskResponse{}, failure(err)
	}
	question, err = s.store.AskQuestion(ctx, question, event, req.ClientID)
	if err != nil {
		return contract.QuestionAskResponse{}, failure(err)
	}
	return contract.QuestionAskResponse{Question: question}, nil
}

// QuestionAnswer ends a question. The session gets the answer as an owner input in its DM, so its reply goes there too.
func (s *Service) QuestionAnswer(ctx context.Context, caller contract.Principal, req contract.QuestionAnswerRequest) (contract.QuestionAnswerResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return contract.QuestionAnswerResponse{}, forbidden()
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return contract.QuestionAnswerResponse{}, invalid("an answer needs text")
	}
	question, err := s.store.Question(ctx, req.ID)
	if err != nil {
		return contract.QuestionAnswerResponse{}, failure(err)
	}
	tree, err := s.store.RoomTree(ctx, question.Room)
	if err != nil {
		return contract.QuestionAnswerResponse{}, failure(err)
	}
	event, err := s.event(question.Room, caller, contract.QuestionAnswered{ID: question.ID, Session: question.Session, Question: question.Text, Answer: text})
	if err != nil {
		return contract.QuestionAnswerResponse{}, failure(err)
	}
	home := contract.Conversation{Kind: contract.ConversationDM, Session: question.Session}
	deliveries := tree.Deliveries(room.DeliveryInput{Event: event, Surface: contract.SurfaceDM, Recipients: []room.Recipient{{Session: question.Session, Explicit: true}}, Home: home})
	if err := s.store.AnswerQuestion(ctx, question.ID, text, event, deliveries); err != nil {
		return contract.QuestionAnswerResponse{}, failure(err)
	}
	return contract.QuestionAnswerResponse{}, nil
}

// QuestionList reads a room's questions. A room's sessions may read them as they read its feed.
func (s *Service) QuestionList(ctx context.Context, caller contract.Principal, req contract.QuestionListRequest) (contract.QuestionListResponse, *contract.Error) {
	if req.Room == "" && caller.Kind == contract.PrincipalSession {
		session, err := s.store.Session(ctx, caller.Session)
		if err != nil {
			return contract.QuestionListResponse{}, failure(err)
		}
		req.Room = session.Room
	}
	if e := s.roomAccess(ctx, caller, req.Room); e != nil {
		return contract.QuestionListResponse{}, e
	}
	questions, err := s.store.Questions(ctx, req.Room)
	if err != nil {
		return contract.QuestionListResponse{}, failure(err)
	}
	return contract.QuestionListResponse{Questions: questions}, nil
}
