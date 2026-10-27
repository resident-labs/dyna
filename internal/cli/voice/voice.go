// Package voice is the dyna voice command: speech for agents.
package voice

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"dyna-agent/internal/engineclient"
	enginevoice "dyna-agent/internal/voice"
)

// Format is an audio file format dyna voice say can write.
type Format string

const (
	WAV Format = "wav"
	OGG Format = "ogg"
	MP3 Format = "mp3"
)

// NewCommand returns the voice command group.
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "voice",
		Short: "Use Dyna's voice from the command line",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(newSayCommand(), newSessionCommand())
	return cmd
}

func newSayCommand() *cobra.Command {
	var req enginevoice.SayRequest
	var out, format string
	cmd := &cobra.Command{
		Use:   "say <text>",
		Short: "Turn text into a speech audio file",
		Long: "Turn text into speech and save it as an audio file, for a demo, a game or an app.\n\n" +
			"The text can have up to 4000 characters. Dyna sends it to the realtime voice provider\n" +
			"chosen in Settings > Voice, or to the one named with --provider, and writes the audio.\n" +
			"The default file format is WAV (16-bit mono PCM). ogg and mp3 need ffmpeg on your PATH.\n\n" +
			"Say never posts to a room, never joins your call and keeps no transcript. It uses your\n" +
			"voice provider account, so each clip counts against that account. At most 2 clips run at\n" +
			"once; a third fails with a busy message. A clip may take up to 60 seconds.\n\n" +
			"A realtime model reads the text exactly in most cases, but it can change a word.\n" +
			"Check the audio when the exact words matter.",
		Example: "  dyna voice say \"Welcome to the server!\" --out hello.wav\n" +
			"  dyna voice say \"Welcome back, traveler.\" --voice marin --style \"a calm old storyteller\" --out hello.ogg --format ogg",
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			req.Text = args[0]
			f, err := parseFormat(format, out)
			if err != nil {
				return err
			}
			if out == "" {
				return errors.New("choose where to save the audio with --out <file>")
			}
			if f != WAV {
				if _, err := exec.LookPath("ffmpeg"); err != nil {
					return fmt.Errorf("--format %s needs ffmpeg, which is not on your PATH; install ffmpeg or use --format wav", f)
				}
			}
			var res enginevoice.SayResult
			if err := engineclient.New("").Call(c.Context(), "VoiceSay", []any{req}, &res); err != nil {
				return fmt.Errorf("the text was not spoken: %w", err)
			}
			if err := writeAudio(c.Context(), out, f, res); err != nil {
				return err
			}
			_, err = fmt.Fprintf(c.OutOrStdout(), "saved %s\n", out)
			return err
		},
	}
	cmd.Flags().StringVar(&req.Voice, "voice", "", "voice name; the provider's presets only (default: the provider's default voice)")
	cmd.Flags().StringVar(&req.Style, "style", "", "how to speak, in plain words, such as \"a cheerful village helper\"")
	cmd.Flags().StringVar(&req.Provider, "provider", "", "voice provider (default: the provider in Settings > Voice)")
	cmd.Flags().StringVar(&out, "out", "", "file to write (required)")
	cmd.Flags().StringVar(&format, "format", "", "wav, ogg or mp3 (default: from the --out extension, else wav)")
	return cmd
}

// parseFormat picks the format from --format, else from the file's
// extension, else WAV.
func parseFormat(format, out string) (Format, error) {
	name := strings.ToLower(strings.TrimSpace(format))
	if name == "" {
		name = strings.TrimPrefix(strings.ToLower(filepath.Ext(out)), ".")
	}
	switch Format(name) {
	case OGG, MP3:
		return Format(name), nil
	case WAV, "":
		return WAV, nil
	}
	return "", fmt.Errorf("unknown format %q; use wav, ogg or mp3, or name the file with a .wav, .ogg or .mp3 extension", name)
}

// writeAudio saves res to path in format f.
func writeAudio(ctx context.Context, path string, f Format, res enginevoice.SayResult) error {
	wav := WAVBytes(res.Audio, res.SampleRate)
	if f == WAV {
		return os.WriteFile(path, wav, 0o644)
	}
	// Encode to a temporary file beside the target, so a failed encode leaves
	// no partial file and a name that starts with "-" is not read as an option.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".dyna-say-*")
	if err != nil {
		return err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	ffmpeg := exec.CommandContext(ctx, "ffmpeg", "-y", "-loglevel", "error", "-f", "wav", "-i", "pipe:0", "-f", string(f), tmp.Name())
	ffmpeg.Stdin = bytes.NewReader(wav)
	if msg, err := ffmpeg.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg could not write %s: %w: %s", f, err, strings.TrimSpace(string(msg)))
	}
	return os.Rename(tmp.Name(), path)
}

// WAVBytes wraps PCM16 mono little-endian audio in a WAV file.
func WAVBytes(pcm []byte, sampleRate int) []byte {
	const channels, bits = 1, 16
	header := make([]byte, 0, 44)
	le := binary.LittleEndian
	u32 := func(v int) { header = le.AppendUint32(header, uint32(v)) }
	u16 := func(v int) { header = le.AppendUint16(header, uint16(v)) }
	header = append(header, "RIFF"...)
	u32(36 + len(pcm))
	header = append(header, "WAVEfmt "...)
	u32(16)
	u16(1) // PCM
	u16(channels)
	u32(sampleRate)
	u32(sampleRate * channels * bits / 8)
	u16(channels * bits / 8)
	u16(bits)
	header = append(header, "data"...)
	u32(len(pcm))
	return append(header, pcm...)
}
