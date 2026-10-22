package workflowsvc

import (
	"context"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/workflow"
)

// ScriptPoster delivers a workflow script's post() as run:<runID>.
func ScriptPoster(ctx context.Context, runID string, mentions []string, text string) (workflow.PostResult, error) {
	svc := notify.Default()
	snd, err := svc.ScriptSender(runID)
	if err != nil {
		return workflow.PostResult{}, err
	}
	res, err := svc.Post(ctx, notify.PostInput{Sender: snd, Mentions: mentions, Text: text})
	out := workflow.PostResult{ID: res.Message.ID}
	for _, d := range res.Deliveries {
		item := map[string]any{"to": d.To, "delivery": d.Delivery}
		if d.Turn > 0 {
			item["turn"] = d.Turn
		}
		if d.Pointer {
			item["pointer"] = true
		}
		if d.Error != "" {
			item["error"] = d.Error
		}
		out.Deliveries = append(out.Deliveries, item)
	}
	for _, sk := range res.Skipped {
		out.Skipped = append(out.Skipped, map[string]any{"to": sk.To, "reason": sk.Reason})
	}
	return out, err
}
