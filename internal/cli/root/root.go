// Package root holds the room's root seat commands: an external orchestrator
// attaches to take the seat and detaches to give it back to the internal root.
package root

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/roomsvc"
	"github.com/spf13/cobra"
)

type Options struct{ Service func() *notify.Service }

func NewCommand() *cobra.Command { return Command(Options{}) }

func Command(o Options) *cobra.Command {
	if o.Service == nil {
		o.Service = notify.Default
	}
	var roomRef, handoff, harness string
	cmd := &cobra.Command{Use: "root", Short: "Attach to or detach from the room's root seat",
		Long: "Each room has one root seat. The internal root session holds it by default, and posts to @root wake it. " +
			"An external orchestrator runs `dyna root attach` to take the seat; posts to @root then wait in the room log for its `dyna room watch`. " +
			"`dyna root detach` gives the seat back to the internal root. Both ask the engine, which checks that the owner runs them; start the engine first if it is not running.",
		Args: cobra.NoArgs}
	cmd.PersistentFlags().StringVar(&roomRef, "room", "", "room name or ID (default: the current repo's room)")
	resolve := func() (room.Record, error) {
		return o.Service().Rooms.ResolveContext(roomRef, false)
	}
	attach := func(c *cobra.Command, _ []string) error {
		rec, err := resolve()
		if err != nil {
			return err
		}
		var out notify.SeatAttach
		if err := engineClient().Call(c.Context(), "AttachSeat", []any{rec.ID, harness}, &out); err != nil {
			return err
		}
		if !out.Changed {
			fmt.Fprint(c.OutOrStdout(), "unchanged: ")
		}
		printSeat(c.OutOrStdout(), rec.Name, out.Seat)
		if out.Changed && out.Note != "" {
			fmt.Fprintf(c.OutOrStdout(), "\n%s\n", out.Note)
		}
		if out.StoppedTurn > 0 {
			fmt.Fprintf(c.OutOrStdout(), "stopped the internal root's turn %d\n", out.StoppedTurn)
		}
		return nil
	}
	detach := func(c *cobra.Command, _ []string) error {
		rec, err := resolve()
		if err != nil {
			return err
		}
		note, err := readHandoff(c, handoff)
		if err != nil {
			return err
		}
		var out roomsvc.SeatReturn
		if err := engineClient().Call(c.Context(), "ReturnSeat", []any{rec.ID, note}, &out); err != nil {
			return err
		}
		if !out.Changed {
			fmt.Fprint(c.OutOrStdout(), "unchanged: ")
		}
		printSeat(c.OutOrStdout(), rec.Name, out.Seat)
		for _, d := range out.Deliveries {
			fmt.Fprintf(c.OutOrStdout(), "handoff: %s %s\n", d.To, d.Delivery)
		}
		return nil
	}
	attachCmd := &cobra.Command{Use: "attach", Short: "Take the root seat as the external orchestrator", Long: "Take the root seat as the external orchestrator. Posts to @root then wait in the room log for your `dyna room watch`. An open internal root gets up to 30 seconds to write a handoff before its turn stops. The handoff, or a transcript fallback, prints here and appears in the seat note. --harness names your harness, so the web app shows its icon.", Args: cobra.NoArgs, RunE: attach}
	attachCmd.Flags().StringVar(&harness, "harness", "", "your harness, one word: claude, codex, opencode, pi, grok, muse or antigravity")
	cmd.AddCommand(attachCmd)
	detachCmd := &cobra.Command{Use: "detach", Short: "Give the root seat back to the internal root", Long: "Give the root seat back to the internal root. Posts you handled while attached never reach it. With --handoff, your note becomes the internal root's next turn input and wakes it; without it, the internal root resumes on the next post.", Args: cobra.NoArgs, RunE: detach}
	detachCmd.Flags().StringVar(&handoff, "handoff", "", "file with a handoff note for the internal root, or - for stdin")
	cmd.AddCommand(detachCmd)
	status := func(c *cobra.Command, _ []string) error {
		rec, err := o.Service().Rooms.ResolveContext(roomRef, false)
		if err != nil {
			return err
		}
		printSeat(c.OutOrStdout(), rec.Name, rec.Seat)
		return nil
	}
	cmd.AddCommand(&cobra.Command{Use: "status", Short: "Show who holds the root seat", Args: cobra.NoArgs, RunE: status})
	cmd.RunE = status
	return cmd
}

func printSeat(w io.Writer, roomName string, seat room.Seat) {
	holder := "internal root"
	if seat.External {
		holder = "external orchestrator"
		if seat.Harness != "" {
			holder += " (" + seat.Harness + ")"
		}
	}
	if seat.Since.IsZero() {
		fmt.Fprintf(w, "room %s: root seat held by the %s\n", roomName, holder)
		return
	}
	fmt.Fprintf(w, "room %s: root seat held by the %s since %s\n", roomName, holder, seat.Since.Local().Format(time.RFC3339))
}

// engineClient reaches the engine, which changes the seat for the owner.
var engineClient = func() *engineclient.Client { return engineclient.New("") }

// readHandoff reads the --handoff note from a file or, for "-", stdin.
func readHandoff(c *cobra.Command, path string) (string, error) {
	if path == "" {
		return "", nil
	}
	var b []byte
	var err error
	if path == "-" {
		b, err = io.ReadAll(c.InOrStdin())
	} else {
		b, err = os.ReadFile(path)
	}
	if err != nil {
		return "", err
	}
	note := strings.TrimSpace(string(b))
	if note == "" {
		return "", fmt.Errorf("the handoff note in %s is empty", path)
	}
	return note, nil
}
