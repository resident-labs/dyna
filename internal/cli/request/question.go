package request

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/questions"
	"dyna-agent/internal/room"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/visual"
	"github.com/spf13/cobra"
)

// newQuestion is dyna request question: root asks the owner and keeps working.
// With no text it lists the caller's open requests.
func newQuestion(roomRef *string, jsonOut *bool) *cobra.Command {
	var meanwhile, to, opts, html string
	var attach []string
	cmd := &cobra.Command{Use: "question [text]", Short: "Root asks the owner a question and keeps working", Long: "Root asks the owner a question and keeps working; the answer arrives as a post. Questions to the owner are root-only: other sessions post to @parent, or file a terminal request with dyna request terminal for a terminal step.", Args: func(c *cobra.Command, args []string) error {
		if mentions, _ := room.SplitLeadingMentions(args); len(mentions) > 0 && len(args) > 1 {
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
			return listMine(c, s, snd, *roomRef, *jsonOut)
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
		rec, err := s.QuestionRoom(snd, *roomRef)
		if err != nil {
			return err
		}
		var q questions.Question
		err = questionCall(c.Context(), "AskQuestion", []any{notify.AskRequest{Room: rec.ID, To: to, Text: args[0], Meanwhile: meanwhile, Options: options, Files: files, Claim: notify.ClaimOf(snd)}}, &q)
		if q.ID != "" && q.Post != "" {
			if *jsonOut {
				_ = json.NewEncoder(c.OutOrStdout()).Encode(q)
			} else {
				fmt.Fprintf(c.OutOrStdout(), "Asked %s. Keep working; the answer arrives as a post.\n", q.ID)
			}
		}
		return err
	}
	cmd.Flags().StringVar(&meanwhile, "meanwhile", "", "useful safe work you will do while you wait (required)")
	cmd.Flags().StringVar(&to, "to", "", "only the owner (@root); ask agents with dyna post")
	cmd.Flags().StringVar(&opts, "options", "", "up to six comma-separated options")
	cmd.Flags().StringVar(&html, "html", "", "attach an HTML card; the card can answer the question with parent.postMessage({type: 'dyna:submit', text, choice?}, '*') after the owner confirms it under the card (see dyna post --help)")
	cmd.Flags().StringArrayVar(&attach, "attach", nil, "attach a file (repeatable)")
	return cmd
}

// newAnswer is dyna request answer. A terminal request refuses it: only the
// owner reports one, from the terminal.
func newAnswer(roomRef *string) *cobra.Command {
	var choice string
	cmd := &cobra.Command{Use: "answer <id> [text]", Short: "Answer all matching open questions", Args: cobra.RangeArgs(1, 2), RunE: func(c *cobra.Command, args []string) error {
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
		rec, err := s.QuestionRoom(snd, *roomRef)
		if err != nil {
			return err
		}
		return questionCall(c.Context(), "AnswerQuestion", []any{rec.ID, args[0], choice, text}, nil)
	}}
	cmd.Flags().StringVar(&choice, "choice", "", "choose an exact option")
	return cmd
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

func questionCall(ctx context.Context, method string, args []any, result any) error {
	if err := engineClient().Call(ctx, method, args, result); err != nil {
		return fmt.Errorf("question change failed: %w", err)
	}
	return nil
}
