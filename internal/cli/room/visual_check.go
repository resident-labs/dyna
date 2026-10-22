package room

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// VisualCmd checks a visual locally using the post-time renderer.
func VisualCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "visual", Short: "Check a visual before posting"}
	var allowNet bool
	check := &cobra.Command{
		Use:   "check <file.html>",
		Short: "Render and check an HTML visual without posting",
		Long: "Run the same renderer and checks as dyna post --html without posting.\n" +
			"Print each check and save the banner, thumbnail, and 360, 768 and 1280 px screenshots.\n" +
			"The output directory is inside DYNA_SCRATCH when set, or the system temporary directory.\n" +
			"Open the images, fix problems, then post. Warnings, failed or skipped checks exit non-zero.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			doc, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			dir, err := os.MkdirTemp(os.Getenv("DYNA_SCRATCH"), "visual-check-")
			if err != nil {
				return err
			}
			dir, err = filepath.Abs(dir)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "Output directory: %s\n", dir)
			result := preview(cmd.Context(), doc, allowNet)
			problem := len(result.Checks) == 0
			for _, check := range result.Checks {
				fmt.Fprintf(w, "%s %s", check.Status, check.Name)
				if check.Detail != "" {
					fmt.Fprintf(w, ": %s", check.Detail)
				}
				fmt.Fprintln(w)
				problem = problem || check.Status != "pass"
			}
			save := func(name string, data []byte) error {
				if len(data) == 0 {
					return nil
				}
				path := filepath.Join(dir, name+".webp")
				if err := os.WriteFile(path, data, 0600); err != nil {
					return err
				}
				fmt.Fprintf(w, "%s: %s\n", name, path)
				return nil
			}
			if err := save("banner", result.Banner); err != nil {
				return err
			}
			if err := save("thumbnail", result.Thumb); err != nil {
				return err
			}
			for _, shot := range result.Widths {
				if err := save(fmt.Sprint(shot.Width), shot.Image); err != nil {
					return err
				}
			}
			if problem {
				return errors.New("visual checks need attention; fix problems before posting")
			}
			return nil
		},
	}
	check.Flags().BoolVar(&allowNet, "allow-net", false, "let the card load images, fonts, scripts and data over HTTPS")
	cmd.AddCommand(check)
	return cmd
}
