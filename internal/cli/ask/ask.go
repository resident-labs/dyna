// Package ask registers async room questions.
package ask

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/questions"
	"dyna-agent/internal/room"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/visual"
	"github.com/spf13/cobra"
)

func NewCommand() *cobra.Command {
	var meanwhile, to, opts, html, roomRef string
	var attach []string
	var jsonOut bool
	cmd := &cobra.Command{Use: "ask [question]", Short: "Root asks the owner a question and keeps working", Args: func(c *cobra.Command, args []string) error {
		if len(args) > 1 && strings.HasPrefix(args[0], "@") {
			return notify.ErrAskAgent
		}
		return cobra.MaximumNArgs(1)(c, args)
	}}
	cmd.RunE = func(c *cobra.Command, args []string) error {
		s := notify.Default()
		s.Sessions.Executable, _ = os.Executable()
		snd, err := s.SelfSender()
		if err != nil {
			return err
		}
		if len(args) == 0 {
			return list(c, s, snd, roomRef, false, true, jsonOut)
		}
		options := []string{}
		if c.Flags().Changed("options") {
			for _, o := range strings.Split(opts, ",") {
				options = append(options, strings.TrimSpace(o))
			}
		}
		if strings.TrimSpace(meanwhile) == "" {
			return fmt.Errorf("--meanwhile is required: say what useful safe work you will do while you wait")
		}
		files, err := readFiles(attach, html)
		if err != nil {
			return err
		}
		rec, err := s.QuestionRoom(snd, roomRef)
		if err != nil {
			return err
		}
		var q questions.Question
		err = questionCall(c.Context(), "AskQuestion", []any{notify.AskRequest{Room: rec.ID, To: to, Text: args[0], Meanwhile: meanwhile, Options: options, Files: files, Claim: notify.ClaimOf(snd)}}, &q)
		if q.ID != "" && q.Post != "" {
			if jsonOut {
				_ = json.NewEncoder(c.OutOrStdout()).Encode(q)
			} else {
				fmt.Fprintf(c.OutOrStdout(), "Asked %s. Keep working; the answer arrives as a post.\n", q.ID)
			}
		}
		return err
	}
	cmd.PersistentFlags().StringVar(&roomRef, "room", "", "room name or ID (defaults to your room)")
	cmd.PersistentFlags().BoolVar(&jsonOut, "json", false, "print JSON")
	cmd.Flags().StringVar(&meanwhile, "meanwhile", "", "useful safe work you will do while you wait (required)")
	cmd.Flags().StringVar(&to, "to", "", "only the owner (@root); ask agents with dyna post")
	cmd.Flags().StringVar(&opts, "options", "", "up to six comma-separated options")
	cmd.Flags().StringVar(&html, "html", "", "attach an HTML card; the card can answer the question with parent.postMessage({type: 'dyna:submit', text, choice?}, '*') after the owner confirms it under the card (see dyna post --help)")
	cmd.Flags().StringArrayVar(&attach, "attach", nil, "attach a file (repeatable)")
	var open bool
	ls := &cobra.Command{Use: "list", Short: "List every question in this room", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		s := notify.Default()
		s.Sessions.Executable, _ = os.Executable()
		snd, err := s.SelfSender()
		if err != nil {
			return err
		}
		return list(c, s, snd, roomRef, open, false, jsonOut)
	}}
	ls.Flags().BoolVar(&open, "open", false, "show only open questions")
	var why, choice string
	wd := &cobra.Command{Use: "withdraw <id>", Short: "Withdraw a question", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		s := notify.Default()
		s.Sessions.Executable, _ = os.Executable()
		snd, err := s.SelfSender()
		if err != nil {
			return err
		}
		rec, err := s.QuestionRoom(snd, roomRef)
		if err != nil {
			return err
		}
		return questionCall(c.Context(), "WithdrawQuestion", []any{rec.ID, args[0], why}, nil)
	}}
	wd.Flags().StringVar(&why, "why", "", "why the question no longer matters")
	answer := &cobra.Command{Use: "answer <id> [text]", Short: "Answer all matching open questions", Args: cobra.RangeArgs(1, 2), RunE: func(c *cobra.Command, args []string) error {
		s := notify.Default()
		s.Sessions.Executable, _ = os.Executable()
		snd, err := s.SelfSender()
		if err != nil {
			return err
		}
		text := ""
		if len(args) > 1 {
			text = args[1]
		}
		rec, err := s.QuestionRoom(snd, roomRef)
		if err != nil {
			return err
		}
		return questionCall(c.Context(), "AnswerQuestion", []any{rec.ID, args[0], choice, text}, nil)
	}}
	answer.Flags().StringVar(&choice, "choice", "", "choose an exact option")
	cmd.AddCommand(ls, wd, answer)
	return cmd
}
func list(c *cobra.Command, s *notify.Service, snd notify.Sender, ref string, open, mine, jsonOut bool) error {
	rec, err := s.QuestionRoom(snd, ref)
	if err != nil {
		return err
	}
	qs, err := s.QuestionStore().List(rec.ID)
	if err != nil {
		return err
	}
	asker := "owner"
	if snd.Kind == notify.SenderSession {
		asker = snd.Session.ID
	}
	out := qs[:0]
	for _, q := range qs {
		if open && q.State != "open" {
			continue
		}
		if mine && (q.Asker != asker || q.State != "open" && (q.Answer == nil || time.Since(q.Answer.At) > 24*time.Hour)) {
			continue
		}
		out = append(out, q)
	}
	if jsonOut {
		return json.NewEncoder(c.OutOrStdout()).Encode(out)
	}
	for _, q := range out {
		fmt.Fprintf(c.OutOrStdout(), "%s  %s  %s", q.ID, q.State, q.Text)
		if q.Answer != nil {
			fmt.Fprintf(c.OutOrStdout(), " — %s %s", q.Answer.Choice, q.Answer.Text)
		}
		fmt.Fprintln(c.OutOrStdout())
	}
	return nil
}
func readFiles(paths []string, html string) ([]notify.File, error) {
	if len(paths)+boolInt(html != "") > room.MaxPostAttachments {
		return nil, fmt.Errorf("a post can carry at most %d attachments", room.MaxPostAttachments)
	}
	limits, err := settings.Content()
	if err != nil {
		return nil, err
	}
	files := []notify.File{}
	for _, p := range paths {
		b, err := settings.ReadContentFile(p, limits.FileBytes)
		if err != nil {
			return nil, err
		}
		files = append(files, notify.File{Name: filepath.Base(p), Data: b})
	}
	if html != "" {
		b, err := settings.ReadContentFile(html, limits.CardBytes)
		if err != nil {
			return nil, err
		}
		files = append(files, notify.File{Name: filepath.Base(html), Data: b, Render: room.RenderHTML, Preview: visual.Preview(context.Background(), b, false)})
	}
	return files, nil
}
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

var engineClient = func() *engineclient.Client { return engineclient.New("") }

func questionCall(ctx context.Context, method string, args []any, result any) error {
	if err := engineClient().Call(ctx, method, args, result); err != nil {
		return fmt.Errorf("question change failed: %w", err)
	}
	return nil
}
