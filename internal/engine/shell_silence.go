package engine

import (
	"context"
	"errors"
	"time"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/shells"
)

// checkShellSilence reports output log silence, not CPU activity or deadlock.
func checkShellSilence(ctx context.Context, n *notify.Service) error {
	v, err := settings.Load(settings.DefaultPath())
	if err != nil || v.ShellSilenceMinutes == 0 {
		return err
	}
	jobs, err := shells.DefaultStore().Silent(time.Now(), time.Duration(v.ShellSilenceMinutes)*time.Minute)
	if err != nil {
		return err
	}
	var errs []error
	for _, job := range jobs {
		label := job.Name
		if label == "" {
			label = "shell"
		}
		_, err := n.ShellSilence(ctx, job.RoomID, job.ID, label, job.Owner, v.ShellSilenceMinutes)
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
