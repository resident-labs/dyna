// Package parentoutput bounds reports sent to callers without changing stored results.
package parentoutput

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const InlineCharLimit = 1500
const PreviewChars = 300

type Reference struct {
	File    string `json:"file"`
	Chars   int    `json:"chars"`
	Preview string `json:"preview"`
}

// Save writes a complete report blob and returns its file reference.
func Save(path, value string) (Reference, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return Reference{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return Reference{}, err
	}
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		return Reference{}, err
	}
	runes := []rune(value)
	return Reference{File: path, Chars: len(runes), Preview: string(runes[:min(len(runes), PreviewChars)])}, nil
}

// JSON replaces long string values, including nested values, in a copy of result.
func JSON(dir, result string) (string, error) {
	if strings.TrimSpace(result) == "" {
		return result, nil
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(result))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	n := 0
	var visit func(any) (any, error)
	visit = func(v any) (any, error) {
		switch v := v.(type) {
		case string:
			if len([]rune(v)) <= InlineCharLimit {
				return v, nil
			}
			n++
			return Save(filepath.Join(dir, "parent-output", fmt.Sprintf("result-%d.txt", n)), v)
		case []any:
			for i := range v {
				next, err := visit(v[i])
				if err != nil {
					return nil, err
				}
				v[i] = next
			}
		case map[string]any:
			keys := make([]string, 0, len(v))
			for key := range v {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				next, err := visit(v[key])
				if err != nil {
					return nil, err
				}
				v[key] = next
			}
		}
		return v, nil
	}
	value, err := visit(value)
	if err != nil {
		return "", err
	}
	if n == 0 {
		return result, nil
	}
	b, err := json.Marshal(value)
	return string(b), err
}

// Text saves the complete report and bounds the post, including its file pointer.
func Text(path, text string) (string, error) {
	if len([]rune(text)) <= InlineCharLimit {
		return text, nil
	}
	ref, err := Save(path, text)
	if err != nil {
		return "", err
	}
	trailer := reportTrailer + ref.File
	budget := InlineCharLimit - len([]rune(trailer))
	if budget < 0 {
		return "", fmt.Errorf("report path exceeds inline character limit")
	}
	return string([]rune(text)[:budget]) + trailer, nil
}

// reportTrailer separates a bounded post from the file Text saved for it.
const reportTrailer = "\n\nFull report: "

// ReportFile returns the file that a post from Text points at: the complete
// report. It reports false for text Text returned unchanged, or for a text
// whose last line names no single path.
func ReportFile(text string) (string, bool) {
	i := strings.LastIndex(text, reportTrailer)
	if i < 0 {
		return "", false
	}
	path := strings.TrimSpace(text[i+len(reportTrailer):])
	if path == "" || strings.ContainsAny(path, "\r\n") {
		return "", false
	}
	return path, true
}
