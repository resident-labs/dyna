package voice

import (
	"context"
	"errors"
	"strings"
)

// Tools connect Voice to the room's execution agent.
type Tools struct {
	link *RoomLink
}

func NewTools(link *RoomLink) *Tools { return &Tools{link: link} }

const workStarted = "accepted. The request is under way; its answer comes later in this call. Wait quietly without another acknowledgement."

// Dispatch sends the owner's exact words to root. The existing room delivery
// starts an idle root or steers its running turn. Repeated words are valid
// steering and are never filtered out here.
func (t *Tools) Dispatch(ctx context.Context, text string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", errors.New("the request is empty")
	}
	if _, err := t.link.Ask(ctx, text); err != nil {
		return "", err
	}
	return workStarted, nil
}
