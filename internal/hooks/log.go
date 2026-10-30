package hooks

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"regexp"
	"strconv"
)

var hookExit = regexp.MustCompile(`exit status (\d+)\s*$`)

// HookRunLog reads the log of a run found through HookRuns. It never opens
// a path the caller supplies.
func (s Service) HookRunLog(repo, name, id string) (HookRunLog, error) {
	runs, err := s.Runs(context.Background(), repo, name)
	if err != nil {
		return HookRunLog{}, err
	}
	for _, r := range runs {
		if r.ID != id {
			continue
		}
		f, err := os.Open(r.LogPath)
		if err != nil {
			return HookRunLog{}, err
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return HookRunLog{}, err
		}
		const max = 8 << 10
		if info.Size() > max {
			if _, err := f.Seek(info.Size()-max, io.SeekStart); err != nil {
				return HookRunLog{}, err
			}
		}
		// Keep the read bounded even if the hook is still writing.
		b, err := io.ReadAll(io.LimitReader(f, max))
		if err != nil {
			return HookRunLog{}, err
		}
		out := HookRunLog{ExitCode: -1}
		if r.Result == ResultPass {
			out.ExitCode = 0
		} else if m := hookExit.FindSubmatch(bytes.TrimSpace(b)); m != nil {
			out.ExitCode, _ = strconv.Atoi(string(m[1]))
		}
		out.Tail = string(b)
		return out, nil
	}
	return HookRunLog{}, errors.New("unknown hook run")
}
