package librarian

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/librarianstate"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/rolesession"
	"dyna-agent/internal/room"
)

// Issues is the room issue board that smells use.
type Issues interface {
	Issues(roomID string, all bool) ([]room.Issue, error)
	FileIssue(roomID string, in room.IssueInput) (room.Issue, error)
}

// Smell is an architectural problem where a design change would make the
// code easier to maintain.
type Smell struct {
	Title     string
	At        []string // file or file:line, relative to the repository root
	Symbols   []string
	Cost      string // what the problem costs in maintenance
	Direction string // a direction, not a patch
}

// FileSmell files a smell on the room issue board after the mechanical
// checks: file references that exist, symbols, cost and direction, no
// matching open issue, and the daily cap.
func (s Service) FileSmell(ctx context.Context, actor authority.Actor, repoDir, by string, sm Smell) (room.Issue, error) {
	r, err := repoid.Resolve(ctx, repoDir)
	if err != nil {
		return room.Issue{}, err
	}
	if err := (librarianstate.Store{DataDir: s.DataDir}).CheckSmell("library smell", r.ID, actor); err != nil {
		return room.Issue{}, err
	}
	sm.Title = strings.TrimSpace(sm.Title)
	if len(sm.At) == 0 {
		return room.Issue{}, fmt.Errorf("a smell needs file references; pass --at file[:line]")
	}
	file, line := "", 0
	var files []room.IssueFile
	for i, at := range sm.At {
		f, n, err := splitAt(at)
		if err != nil {
			return room.Issue{}, err
		}
		if f, err = repoFile(r.Root, f); err != nil {
			return room.Issue{}, err
		}
		files = append(files, room.IssueFile{Path: f, Line: n})
		if n > 0 {
			sm.At[i] = fmt.Sprintf("%s:%d", f, n)
		} else {
			sm.At[i] = f
		}
		if i == 0 {
			file, line = f, n
		}
	}
	if len(sm.Symbols) == 0 || strings.TrimSpace(sm.Cost) == "" || strings.TrimSpace(sm.Direction) == "" {
		return room.Issue{}, fmt.Errorf("a smell needs --symbols, --cost and --direction")
	}
	// Hold the repository's smell lock from the cap and duplicate checks
	// until the count is saved, so concurrent smells cannot pass both.
	unlock, err := rolesession.LockWait(ctx, filepath.Join(s.DataDir, "library"), "librarian-smell", r.ID)
	if err != nil {
		return room.Issue{}, err
	}
	defer unlock()
	states := librarianstate.Store{DataDir: s.DataDir}
	st, err := states.Get(r.ID)
	if err != nil {
		return room.Issue{}, err
	}
	v, err := s.settings()
	if err != nil {
		return room.Issue{}, err
	}
	// library.smellsPerDay caps automatic passes; 0 means no cap. A pass the
	// owner or root started with --now files without the cap.
	day := s.now().UTC().Format("2006-01-02")
	if limit := v.LibrarySmellsPerDay; limit > 0 && !st.SmellUncapped && st.SmellDay == day && st.Smells >= limit {
		return room.Issue{}, fmt.Errorf("the daily cap of %d smells for this repository is reached (library.smellsPerDay)", limit)
	}
	open, err := s.Issues.Issues(st.RoomID, false)
	if err != nil {
		return room.Issue{}, err
	}
	for _, i := range open {
		if sameSmell(i, sm.Title, file) {
			return room.Issue{}, fmt.Errorf("open issue %s already covers this smell: %s", i.Label(), i.Title)
		}
	}
	note := "Files: " + strings.Join(sm.At, ", ") + "\nSymbols: " + strings.Join(sm.Symbols, ", ") +
		"\nCost: " + strings.TrimSpace(sm.Cost) + "\nDirection: " + strings.TrimSpace(sm.Direction) + "\n\nFiled by the librarian."
	issue, err := s.Issues.FileIssue(st.RoomID, room.IssueInput{
		Title: sm.Title, Note: note, File: file, Line: line, Filer: by,
		Files: files, Symbols: sm.Symbols, Cost: strings.TrimSpace(sm.Cost), Direction: strings.TrimSpace(sm.Direction),
	})
	if err != nil {
		return issue, err
	}
	_, err = states.Update(r.ID, false, func(st *librarianstate.State) error {
		if st.SmellDay != day {
			st.SmellDay, st.Smells = day, 0
		}
		st.Smells++
		return nil
	})
	return issue, err
}

// repoFile returns the canonical slash path of a regular file inside root.
// Symlinks are resolved, so an alias of a file inside the repository names
// that file, and a link that leaves the repository is refused.
func repoFile(root, name string) (string, error) {
	refuse := fmt.Errorf("smell reference %s must be a regular file inside the repository", name)
	if filepath.IsAbs(name) {
		return "", refuse
	}
	base, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(filepath.Join(base, filepath.FromSlash(name)))
	if err != nil {
		return "", refuse
	}
	rel, err := filepath.Rel(base, real)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", refuse
	}
	info, err := os.Stat(real)
	if err != nil || !info.Mode().IsRegular() {
		return "", refuse
	}
	return filepath.ToSlash(rel), nil
}

func splitAt(at string) (string, int, error) {
	at = strings.TrimSpace(at)
	if i := strings.LastIndexByte(at, ':'); i > 0 {
		if n, err := strconv.Atoi(at[i+1:]); err == nil {
			if n < 1 {
				return "", 0, fmt.Errorf("line in %s must be positive", at)
			}
			return at[:i], n, nil
		}
	}
	if at == "" {
		return "", 0, fmt.Errorf("empty --at reference")
	}
	return at, 0, nil
}

// sameSmell matches an open issue with the same title words, or one on the
// same file whose title shares at least half its words.
func sameSmell(i room.Issue, title, file string) bool {
	a, b := words(i.Title), words(title)
	shared := 0
	for w := range b {
		if a[w] {
			shared++
		}
	}
	union := len(a) + len(b) - shared
	if union == 0 {
		return false
	}
	if shared == union {
		return true
	}
	return i.File == file && 2*shared >= union
}

func words(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) {
		out[w] = true
	}
	return out
}
