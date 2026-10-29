// Package ciexcerpt cuts the failing part out of a CI log. Local CI saves the
// excerpt when a check finishes, and the readers show it without the full log.
package ciexcerpt

import (
	"regexp"
	"strings"
	"time"

	"dyna-agent/internal/textutil"
)

// Failure returns the failing part of a log text: the failing Go test blocks, or the
// lines around the first failure. The text must already be sanitized.
func Failure(text string, tail bool) string {
	lines := Lines(text)
	if tail {
		if blocks := failedGoBlocks(lines); len(blocks) > 0 {
			return Failure(strings.Join(blocks, "\n"), false)
		}
		failure := -1
		// Prefer the test failure over a package summary followed by passing tests.
		for i, line := range lines {
			if strings.Contains(line, "--- FAIL") {
				failure = i
				break
			}
		}
		if failure < 0 {
			for i, line := range lines {
				for _, word := range strings.Fields(line) {
					if word == "FAIL" {
						failure = i
						break
					}
				}
				if failure >= 0 {
					break
				}
			}
		}
		if failure >= 0 {
			start := max(0, failure-3)
			lines = lines[start:min(len(lines), start+30)]
			tail = false
		} else if len(lines) > 30 {
			lines = lines[len(lines)-30:]
		}
	}
	if !tail && len(lines) > 30 {
		lines = lines[:30]
	}
	text = strings.Join(lines, "\n")
	if len(text) > 4096 {
		if tail {
			text = text[len(text)-4096:]
		} else {
			text = text[:4096]
		}
	}
	return strings.ToValidUTF8(text, "")
}

// ansiColors matches the color and cursor sequences a program writes for a
// terminal: CSI, OSC and the short escape forms.
var ansiColors = regexp.MustCompile("\\x1b\\[[0-9;?]*[ -/]*[@-~]|\\x1b\\][^\\x07\\x1b]*(?:\\x07|\\x1b\\\\)|\\x1b[@-Z\\\\-_]")

// logTimestamp reports whether text starts with the time GitHub writes at the
// start of each log line.
func logTimestamp(text string) bool {
	prefix, _, _ := strings.Cut(text, " ")
	_, err := time.Parse(time.RFC3339Nano, prefix)
	return err == nil
}

// Lines drops what a CI log shows only in a terminal: ANSI colors, the job
// and step columns that gh run view adds, and each line's GitHub timestamp.
func Lines(text string) []string {
	lines := strings.Split(strings.TrimSpace(ansiColors.ReplaceAllString(text, "")), "\n")
	for i, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		if _, rest, ok := strings.Cut(line, "\t"); ok {
			if _, body, ok := strings.Cut(rest, "\t"); ok && logTimestamp(body) {
				line = body
			}
		}
		if _, rest, ok := strings.Cut(line, " "); ok && logTimestamp(line) {
			line = rest
		}
		lines[i] = line
	}
	return lines
}

// Go verbose output places the diagnostic between RUN and FAIL. Parent
// failures repeat their failed children, so keep only the specific test blocks.
func failedGoBlocks(lines []string) []string {
	type block struct {
		name       string
		start, end int
	}
	starts := map[string]int{}
	var blocks []block
	for i, line := range lines {
		text := strings.TrimSpace(line)
		if name, ok := strings.CutPrefix(text, "=== RUN "); ok {
			starts[strings.TrimSpace(name)] = i
		}
		if detail, ok := strings.CutPrefix(text, "--- FAIL: "); ok {
			fields := strings.Fields(detail)
			if len(fields) > 0 {
				if start, found := starts[fields[0]]; found {
					blocks = append(blocks, block{fields[0], start, i + 1})
				}
			}
		}
	}
	var out []string
	size := 0
	for _, b := range blocks {
		parent := false
		for _, other := range blocks {
			if strings.HasPrefix(other.name, b.name+"/") {
				parent = true
				break
			}
		}
		if parent {
			continue
		}
		// Keep the RUN label and the end of the block, where Go prints
		// the diagnostic and FAIL marker. Drop unrelated test progress.
		header := textutil.Prefix(lines[b.start], 300)
		var detail []string
		for _, line := range lines[b.start+1 : b.end] {
			text := strings.TrimSpace(line)
			if strings.HasPrefix(text, "=== ") || strings.HasPrefix(text, "--- PASS:") || strings.HasPrefix(text, "--- SKIP:") {
				continue
			}
			detail = append(detail, line)
		}
		if len(detail) > 29 {
			detail = detail[len(detail)-29:]
		}
		text := strings.Join(detail, "\n")
		remaining := 4096 - len(header) - 1
		if len(text) > remaining {
			text = strings.ToValidUTF8(text[len(text)-remaining:], "")
		}
		part := append([]string{header}, strings.Split(text, "\n")...)
		bytes := len(strings.Join(part, "\n"))
		if len(out) > 0 {
			if size+bytes+2 > 4096 || len(out)+len(part)+1 > 30 {
				break
			}
			out = append(out, "")
			size += 2
		}
		out = append(out, part...)
		size += bytes
	}
	return out
}
