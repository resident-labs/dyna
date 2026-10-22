package computers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

const (
	recordDefaultSeconds = 20
	recordMaxSeconds     = 60
	recordDefaultFPS     = 5
	recordMaxFPS         = 10
	recordDefaultKbps    = 1000
	recordMaxKbps        = 4000
	// maxRecordingBytes stays under the 25 MiB attachment cap (settings.DefaultContentBytes).
	maxRecordingBytes = 24 << 20
)

// recording is a recording in progress. done closes after ffmpeg has finished and the result is stored.
type recording struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// startRecord starts a recording of the whole desktop. Frames come from grim, as for shot, and go to ffmpeg.
// A red box is burned into every frame so the clip itself shows that it was recorded.
func (s *supervisor) startRecord(r Request) (Recording, error) {
	seconds, fps, kbps := orDefault(r.RecordSeconds, recordDefaultSeconds), orDefault(r.RecordFPS, recordDefaultFPS), orDefault(r.RecordKbps, recordDefaultKbps)
	switch {
	case r.RecordSeconds < 0 || r.RecordFPS < 0 || r.RecordKbps < 0:
		return Recording{}, errors.New("recording length, frame rate and bitrate cannot be negative")
	case seconds > recordMaxSeconds:
		return Recording{}, fmt.Errorf("recording length is at most %d seconds", recordMaxSeconds)
	case fps > recordMaxFPS:
		return Recording{}, fmt.Errorf("recording frame rate is at most %d per second", recordMaxFPS)
	case kbps > recordMaxKbps:
		return Recording{}, fmt.Errorf("recording bitrate is at most %d kbit/s", recordMaxKbps)
	case int64(kbps)*1000/8*int64(seconds) > maxRecordingBytes:
		return Recording{}, fmt.Errorf("%d kbit/s for %d seconds can pass the 24 MiB attachment limit; lower the bitrate or the length", kbps, seconds)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.compositor.alive() {
		return Recording{}, errors.New("no windows yet")
	}
	if s.rec != nil {
		return Recording{}, errors.New("already recording; run 'dyna computer record stop' first")
	}
	dir := filepath.Join(Dir(s.meta.SessionID), "recordings")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Recording{}, err
	}
	path := filepath.Join(dir, fmt.Sprintf("%d.mp4", time.Now().UnixNano()))
	w, h := s.state.Width&^1, s.state.Height&^1
	rate := fmt.Sprintf("%dk", kbps)
	ffmpeg := exec.Command("ffmpeg", "-v", "error", "-y", "-use_wallclock_as_timestamps", "1", "-f", "image2pipe", "-c:v", "png", "-i", "-",
		"-vf", fmt.Sprintf("scale=%d:%d,drawbox=x=8:y=8:w=40:h=24:color=red:t=fill,format=yuv420p", w, h),
		"-r", fmt.Sprint(fps), "-c:v", "libx264", "-preset", "veryfast", "-b:v", rate, "-maxrate", rate, "-bufsize", fmt.Sprintf("%dk", 2*kbps),
		"-movflags", "+faststart", path)
	ffmpeg.Env = s.desktopEnv()
	var ffmpegErr bytes.Buffer
	ffmpeg.Stderr = &ffmpegErr
	stdin, err := ffmpeg.StdinPipe()
	if err != nil {
		return Recording{}, err
	}
	if err := startOwned(ffmpeg); err != nil {
		return Recording{}, fmt.Errorf("start ffmpeg: %w", err)
	}
	ctx, cancel := context.WithTimeout(s.ctx, time.Duration(seconds)*time.Second)
	rec := &recording{cancel: cancel, done: make(chan struct{})}
	s.rec = rec
	s.state.Recording = true
	s.lastActivity = time.Now()
	s.record("recording", nil)
	env := s.desktopEnv()
	started := time.Now()
	go func() {
		defer close(rec.done)
		defer cancel()
		tick := time.NewTicker(time.Second / time.Duration(fps))
		defer tick.Stop()
		// Every clip has at least one frame: a frame in flight when stop arrives is still written.
		for {
			frame, err := grimFrame(s.ctx, env)
			if err != nil {
				fmt.Fprintln(&ffmpegErr, err)
				break
			}
			if _, err := stdin.Write(frame); err != nil {
				break
			}
			select {
			case <-ctx.Done():
			case <-tick.C:
				continue
			}
			break
		}
		stdin.Close()
		waitErr := waitOwned(ffmpeg)
		info := Recording{Path: path, Seconds: time.Since(started).Seconds()}
		if st, err := os.Stat(path); err == nil {
			info.Bytes = st.Size()
		}
		if waitErr != nil {
			info.Message = fmt.Sprintf("ffmpeg failed: %v: %s", waitErr, ffmpegErr.String())
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.rec, s.lastRec = nil, info
		s.state.Recording = false
		s.record("recording", nil)
	}()
	return Recording{Path: path, Seconds: float64(seconds), Running: true}, nil
}

func orDefault(v, fallback int) int {
	if v == 0 {
		return fallback
	}
	return v
}

func grimFrame(ctx context.Context, env []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "grim", "-t", "png", "-")
	cmd.Env = env
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := runOwned(cmd); err != nil {
		return nil, fmt.Errorf("capture: %w: %s", err, stderr.String())
	}
	return out.Bytes(), nil
}

// stopRecord ends the recording in progress, waits for the file to finish, and returns it.
// After an automatic stop it returns the last recording.
func (s *supervisor) stopRecord() (Recording, error) {
	s.mu.Lock()
	rec := s.rec
	s.mu.Unlock()
	if rec != nil {
		rec.cancel()
		<-rec.done
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastRec.Path == "" {
		return Recording{}, errors.New("no recording; run 'dyna computer record start' first")
	}
	if s.lastRec.Message != "" {
		return s.lastRec, errors.New(s.lastRec.Message)
	}
	return s.lastRec, nil
}
