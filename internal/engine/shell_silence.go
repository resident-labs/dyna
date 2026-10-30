package engine

import (
	"context"
	"errors"
	"time"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/shells"
)

// checkShellSilence reports continuously observed local output inactivity.
func checkShellSilence(ctx context.Context, n *notify.Service) error {
	v, err := settings.Load(settings.DefaultPath())
	if err != nil || v.ShellSilenceMinutes == 0 || int64(v.ShellSilenceMinutes) > int64((1<<63-1)/time.Minute) {
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
		_, err := n.ShellInactivity(ctx, job.RoomID, job.ID, label, job.Owner, v.ShellSilenceMinutes, job.LastOutput)
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
