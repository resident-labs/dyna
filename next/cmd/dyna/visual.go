package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/resident-labs/dyna/box/visual"
	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
)

func visualCommand() *cobra.Command {
	var allowNet bool
	cmd := &cobra.Command{Use: "visual"}
	check := &cobra.Command{Use: "check FILE", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		data, err := readVisual(args[0])
		if err != nil {
			return err
		}
		if _, _, err = visual.Meta(args[0], data); err != nil {
			return err
		}
		result, err := visual.Inspect(cmd.Context(), data, visualCSP(allowNet), allowNet)
		if err != nil {
			return err
		}
		dir, err := os.MkdirTemp(os.Getenv("DYNA_SCRATCH"), "visual-")
		if err != nil {
			return err
		}
		write := func(name string, data []byte) error {
			path := filepath.Join(dir, name)
			if err := os.WriteFile(path, data, 0600); err != nil {
				return err
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), path)
			return err
		}
		printVisualChecks(cmd.OutOrStdout(), result.Checks)
		if err := write("banner.webp", result.Banner); err != nil {
			return err
		}
		if err := write("thumbnail.webp", result.Thumb); err != nil {
			return err
		}
		for _, shot := range result.Widths {
			if err := write(fmt.Sprintf("%d.webp", shot.Width), shot.Image); err != nil {
				return err
			}
		}
		return nil
	}}
	check.Flags().BoolVar(&allowNet, "allow-net", false, "Allow HTTPS resources")
	cmd.AddCommand(check)
	return cmd
}

func printVisualChecks(w io.Writer, checks []contract.VisualCheck) {
	for _, check := range checks {
		fmt.Fprintf(w, "%s: %s %s\n", check.Name, check.Status, check.Detail)
	}
}

func visualCSP(net bool) string {
	src, script := "data: blob:", "'unsafe-inline'"
	if net {
		src, script = "https: data: blob:", "'unsafe-inline' https:"
	}
	return "default-src 'none'; script-src " + script + "; connect-src " + src + "; img-src " + src +
		"; font-src " + src + "; media-src " + src + "; style-src 'unsafe-inline' " + src +
		"; frame-src 'none'; child-src 'none'; object-src 'none'; worker-src 'none'; base-uri 'none'; form-action 'none'; sandbox allow-scripts"
}

func visualImages(ctx context.Context, c client, result visual.Result, card *contract.VisualCard) error {
	dir, err := os.MkdirTemp("", "dyna-visual-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	upload := func(name string, data []byte) (string, error) {
		if len(data) == 0 {
			return "", nil
		}
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0600); err != nil {
			return "", err
		}
		file, err := c.Upload(ctx, path)
		return file.SHA256, err
	}
	if card.Banner, err = upload("banner.webp", result.Banner); err != nil {
		return err
	}
	if card.Thumbnail, err = upload("thumbnail.webp", result.Thumb); err != nil {
		return err
	}
	for _, shot := range result.Widths {
		file, err := upload(fmt.Sprintf("%d.webp", shot.Width), shot.Image)
		if err != nil {
			return err
		}
		card.Shots = append(card.Shots, contract.VisualShot{Width: shot.Width, File: file})
	}
	card.Checks = result.Checks
	return nil
}

func readVisual(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, (25<<20)+1))
}
