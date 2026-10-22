//go:build linux

package computers

import (
	"context"
	"os"
	"path/filepath"
)

// StreamBackend supplies an on-demand stream process and its encoding offer.
// The daemon owns its lifetime independently of agent input and capture.
type StreamBackend interface {
	Command(context.Context, StreamOptions) ([]string, Stream, error)
}
type StreamOptions struct{ Socket, ConfigDir string }
type wayVNCBackend struct{}

func (wayVNCBackend) Command(ctx context.Context, opts StreamOptions) ([]string, Stream, error) {
	config := filepath.Join(opts.ConfigDir, "wayvnc")
	if err := os.WriteFile(config, []byte("enable_auth=false\n"), 0600); err != nil {
		return nil, Stream{}, err
	}
	gpu := vaapiWorks(ctx)
	args := []string{"wayvnc", "-C", config, "--unix-socket", "--disable-resizing", "--max-fps", "30", "-S", filepath.Join(filepath.Dir(opts.Socket), "ctl.sock")}
	state := Stream{Running: true, Codec: "negotiated (H.264/Tight/ZRLE)", Encoder: "software"}
	if gpu {
		args = append(args, "--gpu")
		state.Encoder = "VA-API available"
	}
	args = append(args, opts.Socket)
	return args, state, nil
}
