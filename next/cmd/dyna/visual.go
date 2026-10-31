package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/resident-labs/dyna/box/visual"
	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
	"golang.org/x/net/html"
)

func visualCommand() *cobra.Command {
	var allowNet bool
	cmd := &cobra.Command{Use: "visual"}
	check := &cobra.Command{Use: "check FILE", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		data, err := readVisual(args[0])
		if err != nil {
			return err
		}
		if _, _, err = visualMeta(args[0], data); err != nil {
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
		for _, c := range result.Checks {
			fmt.Fprintf(cmd.OutOrStdout(), "%s: %s %s\\n", c.Name, c.Status, c.Detail)
		}
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

// visualMeta reads untrusted HTML once, before it is uploaded.
func visualMeta(name string, data []byte) (string, int, error) {
	ext := strings.ToLower(filepath.Ext(name))
	if ext != ".html" && ext != ".htm" {
		return "", 0, fmt.Errorf("visual must be an .html or .htm file")
	}
	if len(data) > 25<<20 {
		return "", 0, fmt.Errorf("visual exceeds 25 MiB")
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return "", 0, fmt.Errorf("visual must be UTF-8 text")
	}
	height := 360
	var title strings.Builder
	inTitle, titled, sized := false, false, false
	z := html.NewTokenizer(bytes.NewReader(data))
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			if z.Err() != io.EOF {
				return "", 0, z.Err()
			}
			break
		}
		t := z.Token()
		switch tt {
		case html.StartTagToken, html.SelfClosingTagToken:
			switch t.Data {
			case "base":
				return "", 0, fmt.Errorf("visual cannot contain a base element")
			case "title":
				inTitle = !titled
			case "meta":
				var name, content, equiv string
				for _, a := range t.Attr {
					switch a.Key {
					case "name":
						name = a.Val
					case "content":
						content = a.Val
					case "http-equiv":
						equiv = a.Val
					}
				}
				if strings.EqualFold(strings.TrimSpace(equiv), "refresh") {
					return "", 0, fmt.Errorf("visual cannot refresh")
				}
				if strings.EqualFold(name, "dyna-height") && !sized {
					sized = true
					if h, err := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(content, "px"))); err == nil {
						height = min(max(h, 120), 1200)
					}
				}
			}
		case html.EndTagToken:
			if t.Data == "title" && inTitle {
				inTitle, titled = false, true
			}
		case html.TextToken:
			if inTitle {
				title.WriteString(t.Data)
			}
		}
	}
	label := strings.Join(strings.Fields(title.String()), " ")
	if label == "" {
		label = filepath.Base(name)
	}
	if len(label) > 255 {
		label = label[:255]
		for !utf8.ValidString(label) {
			label = label[:len(label)-1]
		}
	}
	return label, height, nil
}
