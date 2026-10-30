package workflowsvc

import (
	"context"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/workflow"
)

// ScriptPoster delivers a workflow script's post() as run:<runID>.
func ScriptPoster(ctx context.Context, runID, stepKey string, mentions []string, text string) (workflow.PostResult, error) {
	svc := notify.Default()
	snd, err := svc.ScriptSender(runID)
	if err != nil {
		return workflow.PostResult{}, err
	}
	meta, err := runstore.ReadMeta(runID)
	if err != nil {
		return workflow.PostResult{}, err
	}
	script := &room.ScriptRef{RunID: runID, Workflow: meta.Name}
	res, err := svc.Post(ctx, notify.PostInput{Sender: snd, WorkflowKey: stepKey, Mentions: mentions, Text: text, Script: script})
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
