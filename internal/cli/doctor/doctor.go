// Package doctor is the dyna doctor command: examine a target and read reports.
package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/doctor"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/room"
	"dyna-agent/internal/store"
	"github.com/spf13/cobra"
)

func NewCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "doctor", Short: "Diagnose load and performance problems", Long: "Diagnose load and performance problems from the samples the engine stores each minute. Run it on demand, or from a hook: a schedule hook with --if is a threshold trigger."}
	var cond string
	var cooldown time.Duration
	var asJSON bool
	examine := &cobra.Command{Use: "examine <target>", Short: "Examine a target and report causes with exact commands", Long: "Examine a target (this version: machine) and print one line when it is healthy, or ranked causes with their numbers and the next command. An unhealthy report is saved and posted to the room's root. With --if, a false condition prints nothing and stores nothing, so a schedule hook every minute stays cheap.", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
		if err := authority.Check("doctor examine", authority.Of(c.Context()), nil); err != nil {
			return err
		}
		machine, err := os.Hostname()
		if err != nil {
			return err
		}
		res, err := doctor.Run{DataDir: store.DataDir(), Machine: machine, Target: a[0], Trigger: trigger(), If: cond, Cooldown: cooldown, Now: time.Now(), Post: post}.Do()
		if !res.Ran {
			return err
		}
		if asJSON {
			if jerr := json.NewEncoder(c.OutOrStdout()).Encode(res); jerr != nil {
				return jerr
			}
			return err
		}
		if res.Repeat {
			fmt.Fprintf(c.OutOrStdout(), "Already examined in report %d at %s; read it with `dyna doctor report %d`.\n", res.Report.ID, res.Report.At.Local().Format("15:04"), res.Report.ID)
			return err
		}
		fmt.Fprintln(c.OutOrStdout(), res.Report.Text())
		return err
	}}
	examine.Flags().StringVar(&cond, "if", "", `examine only when this holds, such as "load>2/core for 5m or memory>85%"`)
	examine.Flags().DurationVar(&cooldown, "cooldown", 30*time.Minute, "skip a finding that a report of the same target holds within this time; 0 turns it off")
	examine.Flags().BoolVar(&asJSON, "json", false, "print the result as JSON")
	var reportJSON bool
	report := &cobra.Command{Use: "report [id]", Short: "List recent reports, or print one", Long: "List the 10 newest reports, or print one report in full.", Args: cobra.MaximumNArgs(1), RunE: func(c *cobra.Command, a []string) error {
		if err := authority.Check("doctor report", authority.Of(c.Context()), nil); err != nil {
			return err
		}
		reports := doctor.Reports{DataDir: store.DataDir()}
		var list []doctor.Report
		if len(a) == 1 {
			id, err := strconv.ParseInt(a[0], 10, 64)
			if err != nil {
				return fmt.Errorf("report ID %q is not a number", a[0])
			}
			r, found, err := reports.Get(id)
			if err != nil {
				return err
			}
			if !found {
				return fmt.Errorf("no report %d; list them with `dyna doctor report`", id)
			}
			list = []doctor.Report{r}
		} else {
			var err error
			if list, err = reports.List(10); err != nil {
				return err
			}
		}
		if reportJSON {
			return json.NewEncoder(c.OutOrStdout()).Encode(list)
		}
		for _, r := range list {
			if len(a) == 1 {
				fmt.Fprintf(c.OutOrStdout(), "Report %d, %s, %s, by %s\n%s\n", r.ID, r.At.Local().Format(time.DateTime), r.Target, r.Trigger, r.Text())
			} else {
				fmt.Fprintf(c.OutOrStdout(), "%d\t%s\t%s\t%s\n", r.ID, r.At.Local().Format(time.DateTime), r.Target, r.Summary)
			}
		}
		return nil
	}}
	report.Flags().BoolVar(&reportJSON, "json", false, "print reports as JSON")
	cmd.AddCommand(examine, report)
	return cmd
}

// trigger names what started this run: the hook event, or on demand.
func trigger() string {
	if e := os.Getenv("DYNA_HOOK_EVENT"); e != "" {
		return "hook " + e
	}
	return "on demand"
}

// post sends a report to the stamped room, else to the current repo's room,
// addressed to its owner and root.
func post(r doctor.Report) error {
	rooms := room.DefaultStore()
	var rec room.Record
	var err error
	if stamp, ok := mode.StampFromEnv(); ok {
		ref := stamp.RoomID
		if ref == "" {
			ref = stamp.Room
		}
		rec, err = rooms.Resolve(ref)
	} else {
		rec, err = rooms.EnsureRepoRoom(".", "doctor")
	}
	if err != nil {
		return err
	}
	text := fmt.Sprintf("Doctor report %d (%s):\n%s", r.ID, r.Trigger, r.Text())
	_, err = rooms.Post(rec.ID, room.PostInput{From: rec.Name + ":doctor", To: []string{rec.Name + ":root"}, Kind: "doctor", Wake: true, Text: text})
	return err
}
