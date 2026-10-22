package watch

import (
	"context"
	"encoding/base64"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"dyna-agent/internal/cibudget"
)

type prFile struct {
	Name  string `json:"filename"`
	Patch string `json:"patch"`
	// Content is the file at the PR head. It locates each test's span.
	Content string `json:"-"`
}

// maxTestFileReads bounds content reads per run. Unread files count as changed.
const maxTestFileReads = 30

// prTestFiles reads the test files that the PR changes, with their patches
// and their content at the PR head.
func (g GitHub) prTestFiles(ctx context.Context, r Record, head string) ([]prFile, error) {
	var pages [][]prFile
	if err := g.pages(ctx, refRecord(r), fmt.Sprintf("repos/%s/pulls/%d/files?per_page=100", r.Repo, r.Number), &pages); err != nil {
		return nil, err
	}
	var files []prFile
	for _, page := range pages {
		for _, f := range page {
			base := path.Base(f.Name)
			if !strings.HasSuffix(base, "_test.go") && !strings.Contains(base, ".test.") && !strings.Contains(base, ".spec.") {
				continue
			}
			// Read the head content even without a patch: a large diff has no
			// patch, and only the content proves which tests a file can hold.
			if len(files) < maxTestFileReads {
				var body struct {
					Content string `json:"content"`
				}
				if err := g.api(ctx, refRecord(r), fmt.Sprintf("repos/%s/contents/%s?ref=%s", r.Repo, f.Name, head), &body); err == nil {
					data, _ := base64.StdEncoding.DecodeString(strings.ReplaceAll(body.Content, "\n", ""))
					f.Content = string(data)
				}
			}
			files = append(files, f)
		}
	}
	return files, nil
}

// changedByPR reports whether the PR adds or changes the test: a changed line
// falls inside the test's span in a changed test file. A Go test must be in
// that file's package. A file without a patch counts as changed only if it
// could contain the test. A test reported by file path matches that file.
func changedByPR(t cibudget.Test, files []prFile) bool {
	goTest := strings.HasPrefix(t.Name, "Test") && t.Package != "" && t.Package != "test"
	name := t.Name
	if goTest {
		name, _, _ = strings.Cut(name, "/")
	}
	for _, f := range files {
		if goTest {
			dir := path.Dir(f.Name)
			if !strings.HasSuffix(f.Name, "_test.go") || (t.Package != dir && !strings.HasSuffix(t.Package, "/"+dir)) {
				continue
			}
		} else if strings.HasSuffix(f.Name, "_test.go") {
			continue
		} else if strings.HasSuffix(f.Name, name) {
			return true
		}
		// GitHub omits the patch of a large diff. The file counts as changed
		// only if it could contain the test; a Go test already matched its
		// package above.
		if f.Patch == "" {
			if goTest || f.Content == "" || len(jsTestSpans(f.Content, name)) > 0 {
				return true
			}
			continue
		}
		// Without content there are no spans. A failed read proves nothing, so
		// only a name on an added line or in a hunk header counts.
		if f.Content == "" {
			if patchNames(f.Patch, name, goTest) {
				return true
			}
			continue
		}
		spans := jsTestSpans(f.Content, name)
		if goTest {
			spans = nil
			if first, last := goTestSpan(f.Content, name); first > 0 {
				spans = [][2]int{{first, last}}
			}
		}
		for _, line := range changedLines(f.Patch) {
			for _, span := range spans {
				if line >= span[0] && line <= span[1] {
					return true
				}
			}
		}
	}
	return false
}

// patchNames reports whether an added line or a hunk header holds the whole
// test name: a complete Go identifier, or a complete quoted JavaScript name.
func patchNames(patch, name string, goTest bool) bool {
	ident := regexp.MustCompile(`(^|[^A-Za-z0-9_])` + regexp.QuoteMeta(name) + `($|[^A-Za-z0-9_])`)
	for _, line := range strings.Split(patch, "\n") {
		if !strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "@@") {
			continue
		}
		if goTest && ident.MatchString(line) || !goTest && quotesName(line, name) {
			return true
		}
	}
	return false
}

// quotesName reports whether line holds name as one complete quoted string.
func quotesName(line, name string) bool {
	for _, quote := range []string{"'", "\"", "`"} {
		if strings.Contains(line, quote+strings.ReplaceAll(name, quote, "\\"+quote)+quote) {
			return true
		}
	}
	return false
}

// changedLines lists the head-side lines that a patch adds, plus the line
// where each deletion sits.
func changedLines(patch string) []int {
	var lines []int
	next := 0
	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "@@"):
			var oldStart, newStart int
			if _, err := fmt.Sscanf(line, "@@ -%d", &oldStart); err == nil {
				if i := strings.Index(line, " +"); i >= 0 {
					fmt.Sscanf(line[i+2:], "%d", &newStart)
				}
			}
			next = newStart
		case strings.HasPrefix(line, "+"):
			lines = append(lines, next)
			next++
		case strings.HasPrefix(line, "-"):
			lines = append(lines, next)
		case strings.HasPrefix(line, " "):
			next++
		}
	}
	return lines
}

// goTestSpan returns the first and last line of the named test function.
func goTestSpan(content, name string) (int, int) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", content, parser.SkipObjectResolution)
	if err != nil {
		return 0, 0
	}
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == name {
			return fset.Position(fn.Pos()).Line, fset.Position(fn.End()).Line
		}
	}
	return 0, 0
}

// jsTestSpans returns one span for each line that holds the test's quoted
// name. Each span ends at the line before the next test declaration. Every
// match counts, because a constant or comment can hold the name before the
// test does. Ending at the next declaration, not at a matched bracket, keeps
// comments and regular expressions from cutting a span short.
func jsTestSpans(content, name string) [][2]int {
	lines := strings.Split(content, "\n")
	var spans [][2]int
	for i, line := range lines {
		if !quotesName(line, name) {
			continue
		}
		end := len(lines)
		for j := i + 1; j < len(lines); j++ {
			if jsTestStart.MatchString(lines[j]) {
				end = j
				break
			}
		}
		spans = append(spans, [2]int{i + 1, end})
	}
	return spans
}

var jsTestStart = regexp.MustCompile(`^\s*(?:test|it|describe)(?:\.\w+)*\s*\(`)

// seconds writes a duration in seconds with at most two decimals, such as "2.47 s".
func seconds(d time.Duration) string {
	return strconv.FormatFloat(math.Round(d.Seconds()*100)/100, 'f', -1, 64) + " s"
}
