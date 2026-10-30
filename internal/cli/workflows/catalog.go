package workflows

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"dyna-agent/internal/workflowcatalog"
	"dyna-agent/internal/workflowresolve"
	"github.com/spf13/cobra"
)

func NewWorkflowsCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "workflow", Short: "List, show, and save named workflows"}
	var asJSON bool
	list := &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		dir, err := os.Getwd()
		if err != nil {
			return err
		}
		items, err := workflowcatalog.List(dir, "")
		if err != nil {
			return err
		}
		if asJSON {
			return json.NewEncoder(c.OutOrStdout()).Encode(items)
		}
		for _, item := range items {
			fmt.Fprintf(c.OutOrStdout(), "%s\t%s\t%s\n", item.Name, item.Scope, item.Description)
		}
		return nil
	}}
	list.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	show := &cobra.Command{Use: "show <name>", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
		if !workflowresolve.ValidName(a[0]) {
			return fmt.Errorf("invalid workflow name %q", a[0])
		}
		dir, err := os.Getwd()
		if err != nil {
			return err
		}
		_, source, err := workflowresolve.Resolve(a[0], "", dir, "")
		if err != nil {
			return err
		}
		_, err = io.WriteString(c.OutOrStdout(), source)
		return err
	}}
	var stdin, project, overwrite bool
	save := &cobra.Command{Use: "save <name>", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
		if !stdin {
			return fmt.Errorf("--stdin is required")
		}
		dir, err := os.Getwd()
		if err != nil {
			return err
		}
		source, err := io.ReadAll(c.InOrStdin())
		if err != nil {
			return err
		}
		return workflowcatalog.Save(a[0], string(source), dir, "", project, overwrite)
	}}
	save.Flags().BoolVar(&stdin, "stdin", false, "read source from stdin")
	save.Flags().BoolVar(&project, "project", false, "save in this project")
	save.Flags().BoolVar(&overwrite, "overwrite", false, "replace an existing workflow")
	cmd.AddCommand(NewRunCommand(), NewRunsCommand(), list, show, save)
	return cmd
}
