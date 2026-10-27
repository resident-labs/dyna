package engine

import (
	"context"
	"log"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/questions"
	"dyna-agent/internal/websvc"
)

// questionSender records who answers or withdraws a question. Unlike an
// ask, these calls carry no claim. An unverified caller stays unverified.
func (s services) questionSender(ctx context.Context) (notify.Sender, error) {
	caller, local := websvc.CallerFromContext(ctx)
	return questionRecordSender(caller, local, authority.Of(ctx), enforceCallers, log.Printf, s.rooms.Notify.SenderOf)
}

func questionRecordSender(caller websvc.Caller, local bool, actor authority.Actor, enforce bool, logf func(string, ...any), senderOf func(notify.Claim) (notify.Sender, error)) (notify.Sender, error) {
	as, ok, err := recordedCaller(caller, local, actor, enforce, logf, "changing a question")
	if err != nil {
		return notify.Sender{}, err
	}
	if !ok {
		return notify.Sender{Kind: notify.SenderUnverified}, nil
	}
	as.Room = actor.Room
	snd, err := senderOf(as)
	if err != nil {
		return notify.Sender{}, err
	}
	snd.Human = !local
	return snd, nil
}

func (s services) askQuestion(ctx context.Context, req notify.AskRequest) (questions.Question, error) {
	caller, local := websvc.CallerFromContext(ctx)
	snd, err := postSender(caller, local, authority.Of(ctx), req.Claim, enforceCallers, log.Printf, s.rooms.Notify.SenderOf)
	if err != nil {
		return questions.Question{}, err
	}
	snd.Human = !local
	return s.rooms.Notify.Ask(ctx, snd, req.Room, req.To, req.Text, req.Meanwhile, req.Options, req.Files)
}

func (s services) withdrawQuestion(ctx context.Context, ref, id, reason string) error {
	snd, err := s.questionSender(ctx)
	if err != nil {
		return err
	}
	return s.rooms.Notify.WithdrawQuestion(snd, ref, id, reason)
}

func (s services) answerQuestionFromCard(ctx context.Context, ref, id, choice, text string) error {
	snd, err := s.questionSender(ctx)
	if err != nil {
		return err
	}
	return s.rooms.Notify.AnswerQuestionFromCard(ctx, snd, ref, id, choice, text)
}

func (s services) answerQuestion(ctx context.Context, ref, id, choice, text string) error {
	snd, err := s.questionSender(ctx)
	if err != nil {
		return err
	}
	return s.rooms.Notify.AnswerQuestion(ctx, snd, ref, id, choice, text)
}
