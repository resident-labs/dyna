package workflows

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"dyna-agent/internal/cli/jsonout"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/workflowtemplate"
)

func NewTemplatesCommand() *cobra.Command {
	var templatesDir string
	cmd := &cobra.Command{
		Use:   "templates",
		Short: "List, save, show, copy, and seed reusable workflow starter files",
		Args:  cobra.NoArgs,
		RunE:  func(c *cobra.Command, _ []string) error { return c.Help() },
		Long: "Templates are regular JavaScript starter files stored only in the user\n" +
			"registry (default $XDG_CONFIG_HOME/dyna/templates). The filename is the\n" +
			"stable identity. Listing, saving, showing, copying, and seeding never\n" +
			"execute source and never inspect <work-dir>/examples. Copy writes an\n" +
			"ordinary .js file that you edit and launch with `dyna workflow run <file>`.\n" +
			"Templates are not runnable by name.",
		PersistentPreRunE: func(c *cobra.Command, _ []string) error {
			if strings.TrimSpace(templatesDir) != "" || c.Name() == "seed" {
				return nil
			}
			if _, err := seedTemplates(c.Context(), templatesDir, engineclient.RestartWait(0), engineclient.Notices(nil)); err != nil {
				fmt.Fprintf(c.ErrOrStderr(), "warning: cannot refresh starter templates: %v\n", err)
			}
			return nil
		},
	}

	listJSON := false
	list := &cobra.Command{
		Use:   "list",
		Short: "List starter templates in the user registry",
		RunE: func(c *cobra.Command, _ []string) error {
			cat := templateCatalog(templatesDir)
			items, err := cat.List()
			if err != nil {
				return err
			}
			if listJSON {
				if items == nil {
					items = []workflowtemplate.Info{}
				}
				return jsonout.Write(c.OutOrStdout(), items)
			}
			if len(items) == 0 {
				fmt.Fprintln(c.OutOrStdout(), "No workflow templates found.")
				return nil
			}
			for _, item := range items {
				desc := item.Description
				if desc == "" {
					desc = "-"
				}
				fmt.Fprintf(c.OutOrStdout(), "%-9s %-32s %s\n", item.Source, item.Name, desc)
			}
			return nil
		},
	}
	list.Flags().BoolVar(&listJSON, "json", false, "machine-readable output")

	var saveStdin, saveJSON, saveOverwrite bool
	save := &cobra.Command{
		Use:   "save <name>",
		Short: "Save a starter template from stdin into the user registry",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, a []string) error {
			if !saveStdin {
				return fmt.Errorf("save requires --stdin")
			}
			src, err := io.ReadAll(io.LimitReader(c.InOrStdin(), workflowtemplate.MaxSourceBytes+1))
			if err != nil {
				return err
			}
			result, err := templateCatalog(templatesDir).Save(a[0], src, saveOverwrite)
			if err != nil {
				return err
			}
			if saveJSON {
				return jsonout.Write(c.OutOrStdout(), result)
			}
			action := "saved"
			if result.Overwritten {
				action = "updated"
			}
			fmt.Fprintf(c.OutOrStdout(), "%s %s (%s) to %s\n", action, result.Name, settings.FormatBytes(int64(result.Bytes)), result.Path)
			return nil
		},
	}
	save.Flags().BoolVar(&saveStdin, "stdin", false, "read template source from stdin")
	save.Flags().BoolVar(&saveJSON, "json", false, "machine-readable output")
	save.Flags().BoolVar(&saveOverwrite, "overwrite", false, "replace an existing user-registry template")

	showJSON := false
	show := &cobra.Command{
		Use:   "show <name>",
		Short: "Show a named starter template and its unevaluated source",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, a []string) error {
			result, err := templateCatalog(templatesDir).Show(a[0])
			if err != nil {
				return err
			}
			if showJSON {
				return jsonout.Write(c.OutOrStdout(), result)
			}
			fmt.Fprintf(c.OutOrStdout(), "%s  %s  %s\n", result.Source, result.Name, result.Path)
			if result.Description != "" {
				fmt.Fprintln(c.OutOrStdout(), result.Description)
			}
			fmt.Fprintln(c.OutOrStdout())
			fmt.Fprint(c.OutOrStdout(), result.Content)
			if !strings.HasSuffix(result.Content, "\n") {
				fmt.Fprintln(c.OutOrStdout())
			}
			return nil
		},
	}
	show.Flags().BoolVar(&showJSON, "json", false, "machine-readable output")

	var dest string
	var overwrite, copyJSON bool
	copyCmd := &cobra.Command{
		Use:   "copy <name>",
		Short: "Copy a starter template into an editable .js file",
		Long: "Copy a selected starter into --to PATH as an ordinary JavaScript file.\n" +
			"Edit that file, then launch it with `dyna workflow run <file>`. Copy does not\n" +
			"run the template, commit, or push. Refuse overwrite unless --overwrite\n" +
			"is set.",
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, a []string) error {
			if strings.TrimSpace(dest) == "" {
				return fmt.Errorf("copy requires --to")
			}
			result, err := templateCatalog(templatesDir).Copy(a[0], dest, overwrite)
			if err != nil {
				return err
			}
			if copyJSON {
				return jsonout.Write(c.OutOrStdout(), result)
			}
			action := "copied"
			if result.Overwritten {
				action = "overwrote"
			}
			fmt.Fprintf(c.OutOrStdout(), "%s %s to %s\n", action, result.Name, result.Destination)
			return nil
		},
	}
	copyCmd.Flags().StringVar(&dest, "to", "", "destination .js path (required)")
	copyCmd.Flags().BoolVar(&overwrite, "overwrite", false, "replace an existing destination file")
	copyCmd.Flags().BoolVar(&copyJSON, "json", false, "machine-readable output")

	seedJSON := false
	seed := &cobra.Command{
		Use:   "seed",
		Short: "Install or refresh bundled starter templates in the user registry",
		Long: "Copy shipped starters from the embedded examples/ sources into the user\n" +
			"template registry. Missing files are created. A file whose bytes still\n" +
			"match the last seeded copy is updated when the bundle changes. A\n" +
			"user-modified file is left untouched. Writes are atomic. Seed does not\n" +
			"run templates or restore files to the repository.",
		RunE: func(c *cobra.Command, _ []string) error {
			result, err := seedTemplates(c.Context(), templatesDir)
			if err != nil {
				return err
			}
			if seedJSON {
				return jsonout.Write(c.OutOrStdout(), result)
			}
			fmt.Fprintf(c.OutOrStdout(), "seeded %d written, %d updated, %d unchanged, %d preserved\n",
				len(result.Written), len(result.Updated), len(result.Unchanged), len(result.Preserved))
			return nil
		},
	}
	seed.Flags().BoolVar(&seedJSON, "json", false, "machine-readable output")

	cmd.PersistentFlags().StringVar(&templatesDir, "templates-dir", "", "name-keyed starter registry (default user config)")
	cmd.AddCommand(list, save, show, copyCmd, seed)
	return cmd
}

func templateCatalog(templatesDir string) workflowtemplate.Catalog {
	return workflowtemplate.New(templatesDir)
}

var templateEngineClient = func(opts ...engineclient.Option) *engineclient.Client {
	return engineclient.New("", opts...)
}

func seedTemplates(ctx context.Context, templatesDir string, opts ...engineclient.Option) (workflowtemplate.SeedResult, error) {
	registry, err := filepath.Abs(templateCatalog(templatesDir).RegistryDir)
	if err != nil {
		return workflowtemplate.SeedResult{}, err
	}
	var result workflowtemplate.SeedResult
	err = templateEngineClient(opts...).Call(ctx, "TemplateSeed", []any{registry}, &result)
	return result, err
}
