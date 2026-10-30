package ghclient

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// commentCall is a gh api PATCH or DELETE on one issue or pull request comment.
type commentCall struct {
	repo   string
	kind   string
	id     string
	method string
}

var commentEndpoint = regexp.MustCompile(`^/?repos/([\w.-]+/[\w.-]+)/(issues|pulls)/comments/([0-9]+)$`)

// ownCommentCall reports whether args are a gh api PATCH or DELETE on a comment
// endpoint. Whether the comment is the room account's is checked later, with
// the policy decision.
func ownCommentCall(args []string) (commentCall, bool) {
	if len(args) == 0 || args[0] != "api" {
		return commentCall{}, false
	}
	call := parseAPICall(args)
	positional := parseArgs(args[:1], args[1:]).positional
	if call.graphql || len(positional) != 1 || (call.method != "PATCH" && call.method != "DELETE") {
		return commentCall{}, false
	}
	m := commentEndpoint.FindStringSubmatch(positional[0])
	if m == nil {
		return commentCall{}, false
	}
	return commentCall{repo: m[1], kind: m[2], id: m[3], method: call.method}, true
}

var errInputBody = errors.New("a comment edit must pass its text as -f body=..., so Dyna can check it and add the attribution line; --input is not allowed")

// apiBody reads the body field of a gh api comment edit. It returns the
// arguments without that field, the body, and whether the field was there.
func apiBody(cmd *exec.Cmd, args []string) (rest []string, body string, found bool, err error) {
	rest = []string{args[0]}
	for i := 1; i < len(args); i++ {
		arg := args[i]
		name, value, attached := strings.Cut(arg, "=")
		short, shortValue, isShort := shortFlag(arg)
		switch {
		case arg == "--input" || name == "--input":
			return nil, "", false, errInputBody
		case arg == "-f" || arg == "-F" || arg == "--field" || arg == "--raw-field":
			if i+1 >= len(args) {
				rest = append(rest, arg)
				continue
			}
			i++
			name, value = arg, args[i]
		case attached && (name == "--field" || name == "--raw-field"):
		case isShort && (short == "-f" || short == "-F"):
			name, value = short, shortValue
		default:
			rest = append(rest, arg)
			continue
		}
		key, text, _ := strings.Cut(value, "=")
		if key != "body" {
			if name == "-f" || name == "-F" || name == "--field" || name == "--raw-field" {
				rest = append(rest, name, value)
			} else {
				rest = append(rest, arg)
			}
			continue
		}
		found = true
		body = text
		if name == "-F" || name == "--field" {
			if file, ok := strings.CutPrefix(text, "@"); ok {
				data, err := readBodyFile(cmd, file)
				if err != nil {
					return nil, "", false, err
				}
				body = string(data)
			}
		}
	}
	return rest, body, found, nil
}

// readBodyFile reads a body file; "-" reads standard input and puts it back.
func readBodyFile(cmd *exec.Cmd, file string) ([]byte, error) {
	if file == "-" {
		if cmd.Stdin == nil {
			return nil, nil
		}
		data, err := io.ReadAll(cmd.Stdin)
		cmd.Stdin = bytes.NewReader(data)
		return data, err
	}
	if !filepath.IsAbs(file) {
		file = filepath.Join(cmd.Dir, file)
	}
	return os.ReadFile(file)
}
