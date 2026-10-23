package configsvc

import (
	"context"
	"fmt"

	"dyna-agent/internal/settings"
	"dyna-agent/internal/store"
	selfupdate "dyna-agent/internal/update"
)

// UpdateNotice is a quiet line about a newer dyna release. It only checks;
// installing stays an explicit `dyna update`, so a window never replaces
// the binary its sessions run under.
type UpdateNotice struct {
	Current string `json:"current"`
	Latest  string `json:"latest"`
	Text    string `json:"text"`
}

// CheckUpdate compares the installed CLI version with the newest release. It
// shares `dyna update`'s cache, so the network is asked at most once a day.
// It returns an empty notice when updater.autoCheck is off, when disabled is
// set (DYNA_NO_AUTO_UPDATE=1), for a development build, or when the CLI is
// current.
func CheckUpdate(ctx context.Context, version string, disabled bool) (UpdateNotice, error) {
	v, err := settings.Load(settings.DefaultPath())
	if err != nil || disabled || !v.AutoCheck || version == "" || version == "dev" {
		return UpdateNotice{}, err
	}
	res, err := selfupdate.Config{
		Version: version,
		Repo:    selfupdate.DefaultRepo,
		DataDir: store.DataDir(),
	}.Check(ctx, true)
	if err != nil || !res.Available {
		return UpdateNotice{}, err
	}
	return UpdateNotice{Current: res.Current, Latest: res.Latest, Text: fmt.Sprintf("dyna %s is available (you have %s). Run `dyna update` to install it.", res.Latest, res.Current)}, nil
}
