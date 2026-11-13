package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/coder/websocket"
	"github.com/resident-labs/dyna/contract"
)

func (s *Service) VoiceStatus(ctx context.Context, p contract.Principal, _ contract.VoiceStatusRequest) (contract.VoiceStatusResponse, *contract.Error) {
	if p.Kind != contract.PrincipalOwner {
		return contract.VoiceStatusResponse{}, forbidden()
	}
	return contract.VoiceStatusResponse{Room: contract.RoomID(s.voice.Status())}, nil
}
func (s *Service) VoiceStop(ctx context.Context, p contract.Principal, _ contract.VoiceStopRequest) (contract.VoiceStopResponse, *contract.Error) {
	if p.Kind != contract.PrincipalOwner {
		return contract.VoiceStopResponse{}, forbidden()
	}
	s.voice.Stop()
	return contract.VoiceStopResponse{}, nil
}

// Voice prepares the room before upgrading the page connection. The returned function owns its subscription.
func (s *Service) Voice(ctx context.Context, room contract.RoomID) (func(*websocket.Conn) error, *contract.Error) {
	tree, err := s.store.RoomTree(ctx, room)
	if err != nil {
		return nil, failure(err)
	}
	root := tree.Root()
	if root == "" {
		return nil, invalid("room has no root")
	}
	if s.searchProxy == nil {
		return nil, invalid("model proxy unavailable")
	}
	ids, err := s.store.EligibleAccountProxyIDs(ctx, room)
	if err != nil {
		return nil, failure(err)
	}
	if len(ids) == 0 {
		return nil, invalid("room has no eligible model accounts")
	}
	return func(page *websocket.Conn) error {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		// Subscribe before taking the cursor so commits cannot fall between replay and live notification.
		changes, unsubscribe := s.store.Subscribe()

		latest, err := s.store.EventsPage(ctx, room, 0, 1<<62, 1, nil, "")
		if err != nil {
			unsubscribe()
			return err
		}
		var after int64
		if len(latest) > 0 {
			after = latest[0].Seq
		}
		questions, err := s.store.Questions(ctx, room)
		if err != nil {
			unsubscribe()
			return err
		}
		var handoffMu sync.Mutex
		var handoffInput contract.EventID
		var handoffTurn contract.TurnID
		var handoffResult chan string
		backend := make(chan string)
		go func() {
			defer cancel()
			defer func() { unsubscribe() }()
			defer close(backend)
			seen := make(map[contract.QuestionID]bool)
			for _, q := range questions {
				if q.Session != root || q.State != contract.QuestionStatePending {
					continue
				}
				seen[q.ID] = true
				select {
				case backend <- fmt.Sprintf("Question %s: %s", q.ID, q.Text):
				case <-ctx.Done():
					return
				}
			}
			for {
				events, err := s.store.Events(ctx, room, after, 256, nil)
				if err != nil {
					return
				}
				for _, event := range events {
					after = event.Seq
					text := ""
					handoffMu.Lock()
					switch b := event.Body.(type) {
					case contract.TurnStarted:
						if b.Session == root && handoffResult != nil && slices.Contains(b.Inputs, handoffInput) {
							handoffTurn = b.Turn
						}
					case contract.PostCreated:
						if event.From.Session == root && b.Home.Kind == contract.ConversationDM && b.Home.Session == root {
							text = b.Text
						}
					case contract.TurnEnded:
						// A turn can start between the idle snapshot and posting. In
						// that case the new input steers it without another turn.started.
						if b.Session == root && handoffResult != nil && handoffTurn == "" {
							took, err := s.store.TurnTookPost(ctx, root, b.Turn, handoffInput)
							if err != nil {
								handoffMu.Unlock()
								return
							}
							if took {
								handoffTurn = b.Turn
							}
						}
						if b.Session == root && handoffResult != nil && b.Turn == handoffTurn {
							final := b.Final
							for _, reply := range b.Replies {
								if reply.Posted != "" && reply.Reply.Home.Kind == contract.ConversationDM && reply.Reply.Home.Session == root {
									post, err := s.store.EventInRoom(ctx, room, reply.Posted)
									if err != nil {
										handoffMu.Unlock()
										return
									}
									if body, ok := post.Body.(contract.PostCreated); ok {
										final = body.Text
									}
									break
								}
							}
							handoffResult <- final
							handoffResult = nil
							handoffInput = ""
							handoffTurn = ""
						} else if b.Session == root {
							for _, reply := range b.Replies {
								if reply.Posted == "" && reply.Reply.Home.Kind == contract.ConversationDM && reply.Reply.Home.Session == root {
									text = b.Final
									break
								}
							}
						}
					case contract.QuestionAsked:
						if b.Session == root && !seen[b.ID] {
							seen[b.ID] = true
							text = fmt.Sprintf("Question %s: %s", b.ID, b.Text)
						}
					}
					handoffMu.Unlock()
					if text != "" {
						select {
						case backend <- text:
						case <-ctx.Done():
							return
						}
					}
				}
				if len(events) == 256 {
					continue
				}
				select {
				case <-ctx.Done():
					return
				case _, ok := <-changes:
					if !ok {
						unsubscribe()
						changes, unsubscribe = s.store.Subscribe()
						if ctx.Err() != nil {
							return
						}
					}
				}
			}
		}()
		dispatch := func(ctx context.Context, text string) (string, error) {
			handoffMu.Lock()
			session, err := s.store.Session(ctx, root)
			if err != nil {
				handoffMu.Unlock()
				return "", err
			}
			busy := session.Turn != "" || handoffResult != nil
			result, problem := s.Post(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.PostRequest{Room: room, Text: text, Spoken: true, Home: contract.Conversation{Kind: contract.ConversationDM, Session: root}})
			if problem != nil {
				handoffMu.Unlock()
				return "", errors.New(problem.Message)
			}
			if busy {
				handoffMu.Unlock()
				return "This was sent to steer the previous background agent task.", nil
			}
			answer := make(chan string, 1)
			handoffInput, handoffResult = result.Event, answer
			handoffMu.Unlock()
			select {
			case final := <-answer:
				return final, nil
			case <-ctx.Done():
				handoffMu.Lock()
				if handoffResult == answer {
					handoffResult = nil
					handoffInput = ""
					handoffTurn = ""
				}
				handoffMu.Unlock()
				return "", ctx.Err()
			}
		}
		return s.voice.Start(ctx, string(room), page, func(ctx context.Context) (*websocket.Conn, error) { return s.searchProxy.DialRealtime(ctx, ids) }, dispatch, backend)
	}, nil
}
