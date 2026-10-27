package notify

import (
	"context"
	"strings"
	"testing"
)

func TestAskAuthorityUsesSender(t *testing.T) {
	svc := &Service{}
	for _, sender := range []Sender{{Kind: SenderOwner, Human: true}, {Kind: SenderScript}} {
		_, err := svc.Ask(context.Background(), sender, "", "", "Question?", "work", nil, nil)
		if err == nil || !strings.HasPrefix(err.Error(), "request question is root only;") || !strings.Contains(err.Error(), "file a terminal request with dyna request terminal") {
			t.Fatalf("sender %v: %v", sender.Kind, err)
		}
	}
}
