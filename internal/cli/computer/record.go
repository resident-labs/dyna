package computer

import (
	"fmt"

	"dyna-agent/internal/computers"
	"github.com/spf13/cobra"
)

const recordWarning = "A recording captures everything on the computer's screen, including secrets such as tokens, passwords and personal data. Check the screen before you start. A red box shows in every frame."

// maxPostBytes is the room attachment limit (25 MiB) minus a margin.
const maxPostBytes = 24 << 20

// newRecordCommand records the whole computer screen. 'dyna computer record' and 'dyna browser record' both use it.
func newRecordCommand(ensure func(*cobra.Command) (computers.Meta, error)) *cobra.Command {
	root := &cobra.Command{Use: "record", Short: "Record your computer's screen to an MP4 clip", Long: "Record your computer's whole screen (any app) to an MP4 clip of at most 60 seconds.\n\n" + recordWarning}
	var seconds, fps, kbps int
	start := &cobra.Command{Use: "start", Short: "Start recording", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		m, err := ensure(c)
		if err != nil {
			return err
		}
		v, err := computers.NewClient(m.SessionID).RecordStart(c.Context(), seconds, fps, kbps)
		if err != nil {
			return err
		}
		fmt.Fprintln(c.ErrOrStderr(), recordWarning)
		return printJSON(c, v)
	}}
	start.Flags().IntVar(&seconds, "max", 0, "maximum length in seconds (default 20, at most 60)")
	start.Flags().IntVar(&fps, "fps", 0, "frames per second (default 5, at most 10)")
	start.Flags().IntVar(&kbps, "bitrate", 0, "video bitrate in kbit/s (default 1000, at most 4000)")
	var post []string
	stop := &cobra.Command{Use: "stop", Short: "Stop recording and print the clip path, length and size", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		m, err := ensure(c)
		if err != nil {
			return err
		}
		v, err := computers.NewClient(m.SessionID).RecordStop(c.Context())
		if err != nil {
			return err
		}
		if len(post) > 0 {
			if v.Bytes > maxPostBytes {
				return fmt.Errorf("recording is %d bytes, over the 25 MiB attachment limit; record a shorter clip or lower --bitrate", v.Bytes)
			}
			if err := postAttachment(c, post, "Computer recording", v.Path); err != nil {
				return err
			}
		}
		return printJSON(c, v)
	}}
	stop.Flags().StringArrayVar(&post, "post", nil, "post the clip to a room mention (repeatable)")
	root.AddCommand(start, stop)
	return root
}
