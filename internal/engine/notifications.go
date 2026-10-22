package engine

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"dyna-agent/internal/childenv"
	"dyna-agent/internal/room"
	"dyna-agent/internal/roomsvc"
)

// The desktop notification service applies the owner's system settings.
func notifyQuestions(ev roomsvc.Event) {
	if ev.Kind != "posts" {
		return
	}
	for _, p := range ev.Posts {
		if p.QuestionID() == "" || p.Owner || p.Human {
			continue
		}
		owner := false
		for _, to := range p.To {
			if !strings.Contains(to, ":") {
				owner = true
			}
		}
		if !owner {
			continue
		}
		text := p.Text
		if _, rest, ok := strings.Cut(text, "] "); ok {
			text = strings.SplitN(rest, "\n", 2)[0]
		}
		go func(p roomsvc.Post, text string) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = childenv.Clean(exec.CommandContext(ctx, "notify-send", "--app-name=Dyna", p.Speaker+" asked a question", room.TruncateUTF8(text, 240))).Run()
		}(p, text)
	}
}
