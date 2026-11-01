package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/resident-labs/dyna/contract"
)

// workflowMeta is what a script declares before anything starts.
type workflowMeta struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Profiles    []contract.ProfileName `json:"profiles"`
	Size        contract.BoxSizeName   `json:"size"`
	Repo        contract.RepoName      `json:"repo"`
}

var (
	metaStart = regexp.MustCompile(`^export\s+const\s+meta\s*=`)
	metaWord  = regexp.MustCompile(`^[-+.\w$]+`)
	metaValue = regexp.MustCompile(`^(true|false|null|-?[0-9][0-9.eE+-]*)$`)
)

// parseWorkflowMeta reads the literal in `export const meta = {...}` without running the script: scripts never run in the
// engine, and the meta has to be checked before any box or model is spent on it.
func parseWorkflowMeta(script string) (workflowMeta, error) {
	var meta workflowMeta
	text := skipSpace(script)
	start := metaStart.FindString(text)
	if start == "" {
		return meta, errors.New("a script begins with export const meta = {...}")
	}
	literal, rest, err := metaJSON(skipSpace(text[len(start):]))
	if err != nil {
		return meta, fmt.Errorf("meta must be a pure literal: %w", err)
	}
	if line, _, _ := strings.Cut(rest, "\n"); strings.Trim(skipSpace(strings.TrimLeft(line, " \t;")), " \t\r") != "" {
		return meta, errors.New("meta must be a pure literal: it continues past the object")
	}
	decoder := json.NewDecoder(bytes.NewReader(literal))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&meta); err != nil {
		return meta, fmt.Errorf("meta: %w", err)
	}
	if meta.Name == "" || len(meta.Profiles) == 0 {
		return meta, errors.New("meta needs a name and a list of profiles")
	}
	return meta, nil
}

// skipSpace passes whitespace and comments.
func skipSpace(text string) string {
	for {
		text = strings.TrimLeft(text, " \t\r\n")
		switch {
		case strings.HasPrefix(text, "//"):
			_, text, _ = strings.Cut(text, "\n")
		case strings.HasPrefix(text, "/*"):
			_, text, _ = strings.Cut(text, "*/")
		default:
			return text
		}
	}
}

// metaJSON rewrites one JavaScript literal as JSON, and returns the text after it. It holds what JSON cannot: bare keys, other
// quotes, comments and trailing commas. Anything that would have to run is not one of those, and JSON's decoder refuses it.
func metaJSON(text string) ([]byte, string, error) {
	var out []byte
	depth := 0
	for {
		text = skipSpace(text)
		if text == "" {
			return nil, "", errors.New("it does not end")
		}
		switch c := text[0]; c {
		case '{', '[', ':', ',':
			out = append(out, c)
			text = text[1:]
			if c == '{' || c == '[' {
				depth++
			}
		case '}', ']':
			out = append(bytes.TrimRight(out, ","), c)
			text = text[1:]
			if depth--; depth == 0 {
				return out, text, nil
			}
		case '"', '\'', '`':
			value, rest, err := metaString(text)
			if err != nil {
				return nil, "", err
			}
			out = append(out, value...)
			text = rest
		default:
			word := metaWord.FindString(text)
			if word == "" {
				return nil, "", fmt.Errorf("unexpected %q", c)
			}
			text = text[len(word):]
			switch key := strings.HasPrefix(skipSpace(text), ":"); {
			case key:
				out = strconv.AppendQuote(out, word)
			case metaValue.MatchString(word):
				out = append(out, word...)
			default:
				return nil, "", fmt.Errorf("unexpected %q", word)
			}
		}
	}
}

// metaString reads one quoted string as JSON. A template with a value in it has to run, so it is refused.
func metaString(text string) ([]byte, string, error) {
	quote := text[0]
	var value strings.Builder
	for i := 1; i < len(text); i++ {
		switch c := text[i]; {
		case c == quote:
			out, err := json.Marshal(value.String())
			return out, text[i+1:], err
		case c == '$' && quote == '`' && strings.HasPrefix(text[i:], "${"):
			return nil, "", errors.New("a template holds a value")
		case c == '\\' && i+1 < len(text):
			i++
			switch text[i] {
			case 'n':
				value.WriteByte('\n')
			case 't':
				value.WriteByte('\t')
			default:
				value.WriteByte(text[i])
			}
		default:
			value.WriteByte(c)
		}
	}
	return nil, "", errors.New("a string does not end")
}
