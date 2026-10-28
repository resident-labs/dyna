package ghclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/room"
)

type roomsKey struct{}
type roomKey struct{}

// WithRoom binds access checks to the acting room, including independent clones.
func WithRoom(ctx context.Context, ref string) context.Context {
	return context.WithValue(ctx, roomKey{}, ref)
}

// WithRooms uses the caller's room store for access checks.
func WithRooms(ctx context.Context, rooms *room.Store) context.Context {
	return context.WithValue(ctx, roomsKey{}, rooms)
}

const ReadOnly = "read-only"
const ReadWrite = "read-write"

// ValidateAccess checks the room write policy.
func ValidateAccess(value string) error {
	return room.ValidateGitHubAccess(room.GitHubAccess(value))
}

// SetAccess saves the one room write policy shared by the UI and CLI.
func SetAccess(rooms *room.Store, ref, value string) error {
	if err := authority.Check("SetGitHubAccess", authority.Current(), nil); err != nil {
		return err
	}
	if err := ValidateAccess(value); err != nil {
		return err
	}
	return rooms.UpdatePreferences(ref, func(r *room.Record) error { r.GitHubAccess = room.GitHubAccess(value); return nil })
}

// Access returns the room's stored policy. Missing or invalid values deny writes.
func Access(_ context.Context, rec room.Record) string {
	if rec.GitHubAccess == room.GitHubReadWrite {
		return ReadWrite
	}
	return ReadOnly
}

// errNotInRoom is the refusal for a repository that is not the acting room's.
var errNotInRoom = errors.New("GitHub repository does not belong to the acting room")

// outsideReadError explains a refused read of a repository outside the room. Only
// a REST read of a public repository outside every room is allowed (D-272), so
// the message says what that rule needs. Any other error stays as it is.
func outsideReadError(r repository, args []string, err error) error {
	if !errors.Is(err, errNotInRoom) || !isRead(args) {
		return err
	}
	if names, ok := publicReadTargets(r, args); ok {
		return fmt.Errorf("%w: reads outside the room work only for public repositories, and %s could not be confirmed public: it is private or missing, or GitHub did not answer", err, strings.Join(names, ", "))
	}
	return fmt.Errorf("%w: reads outside the room work only for public repositories, as a plain gh api repos/<owner>/<repo> read or a gh api search/... query limited to repo: names, on github.com (gh pr list and gh search need an account, so use gh api repos/<owner>/<repo>/pulls or gh api search/issues)", err)
}

// roomFor checks local room metadata only. It never discovers GitHub repositories.
func roomFor(ctx context.Context, cmdDir string, r repository) (room.Record, error) {
	if cmdDir == "" {
		cmdDir, _ = os.Getwd()
	}
	rooms, _ := ctx.Value(roomsKey{}).(*room.Store)
	if rooms == nil {
		rooms = room.DefaultStore()
	}
	records, err := rooms.List(true)
	if err != nil {
		return room.Record{}, err
	}
	ref, _ := ctx.Value(roomKey{}).(string)
	if ref != "" {
		rec, err := rooms.Resolve(ref)
		if err != nil {
			return room.Record{}, err
		}
		if matchesRepository(ctx, rec, r) {
			return rec, nil
		}
		if r.name == "" && sameCheckout(cmdDir, rec.Repo) {
			return rec, nil
		}
		// The remote read fails when the context ends; that proves no mismatch.
		if err := ctx.Err(); err != nil {
			return room.Record{}, err
		}
		return room.Record{}, errNotInRoom
	}
	var matches []room.Record
	for _, rec := range records {
		if rec.Repo == "" {
			continue
		}
		if sameCheckout(cmdDir, rec.Repo) && (r.name == "" || matchesRepository(ctx, rec, r)) {
			return rec, nil
		}
	}
	for _, rec := range records {
		if matchesRepository(ctx, rec, r) {
			matches = append(matches, rec)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return room.Record{}, errors.New("GitHub repository belongs to multiple rooms; an acting room or checkout is required")
	}
	return room.Record{}, errors.New("GitHub access requires a Dyna room for this repository")
}

func sameCheckout(dir, root string) bool {
	if dir == "" || root == "" {
		return false
	}
	if filepath.Clean(dir) == filepath.Clean(root) {
		return true
	}
	canonical, err := room.RepoRoot(dir)
	return err == nil && canonical == root
}

func matchesRepository(ctx context.Context, rec room.Record, r repository) bool {
	if rec.Repo == "" || r.name == "" {
		return false
	}
	remote, err := repoid.Git(ctx, rec.Repo, "remote", "get-url", "origin")
	if err != nil {
		return false
	}
	identity, err := repoid.RemoteIdentity(rec.Repo, remote)
	return err == nil && strings.EqualFold(identity, r.host+"/"+r.name)
}

var mutation = regexp.MustCompile(`\bmutation\b`)
var errAgentGitHubWrite = errors.New("GitHub writes are not allowed for agents; post in the room with dyna post instead")

func commandArgs(args []string) []string {
	for len(args) > 0 {
		if (args[0] == "--repo" || args[0] == "-R") && len(args) > 1 {
			args = args[2:]
			continue
		}
		if strings.HasPrefix(args[0], "--repo=") || (strings.HasPrefix(args[0], "-R") && len(args[0]) > 2) {
			args = args[1:]
			continue
		}
		break
	}
	return args
}

func isRead(args []string) bool {
	args = commandArgs(args)
	if len(args) == 0 {
		return false
	}
	if len(args) == 1 && args[0] == "--version" {
		return true
	}
	if len(args) >= 2 {
		switch args[0] + " " + args[1] {
		case "repo view", "pr list", "pr view", "pr diff", "pr checks", "issue list", "issue view", "run list", "run view", "run download":
			return true
		}
	}
	if args[0] != "api" {
		return false
	}
	method := ""
	graphql := false
	fields := false
	query := ""
	for i, arg := range args {
		if arg == "graphql" {
			graphql = true
		}
		if strings.HasPrefix(arg, "--method=") {
			method = strings.ToUpper(strings.TrimPrefix(arg, "--method="))
		}
		if strings.HasPrefix(arg, "-X") && len(arg) > 2 {
			method = strings.ToUpper(arg[2:])
		}
		if (arg == "--method" || arg == "-X") && i+1 < len(args) {
			method = strings.ToUpper(args[i+1])
		}
		if arg == "--input" || strings.HasPrefix(arg, "--input=") {
			fields = true
		}
		if strings.HasPrefix(arg, "--field=") || strings.HasPrefix(arg, "--raw-field=") || strings.HasPrefix(arg, "-f") || strings.HasPrefix(arg, "-F") || arg == "--field" || arg == "--raw-field" {
			fields = true
		}
		if strings.HasPrefix(arg, "query=") {
			query = strings.TrimSpace(strings.TrimPrefix(arg, "query="))
		}
	}
	if graphql {
		return (method == "" || method == "GET" || method == "POST") && (strings.HasPrefix(query, "query") || strings.HasPrefix(query, "{")) && !mutation.MatchString(query)
	}
	return method == "GET" || method == "HEAD" || (method == "" && !fields)
}

// publicRepo reports whether a repository is public. Tests replace it.
var publicRepo = fetchPublicRepo

// publicAPIRead returns the repository of a REST read, `gh api repos/<o>/<r>/...`
// with a GET and no body, when that repository is public right now (D-272). The
// repository comes from the endpoint the command reads, never from the checkout,
// so the check binds to what the command fetches. GraphQL, other endpoints and
// every other gh command are not public reads: they need an account.
func publicAPIRead(ctx context.Context, r repository, args []string) (string, bool) {
	names, ok := publicReadTargets(r, args)
	if !ok {
		return "", false
	}
	for _, name := range names {
		if !publicRepo(ctx, repository{"github.com", name}) {
			return "", false
		}
	}
	return names[0], true
}

// searchTargets returns the repositories a search query is limited to. It
// reports false when the query names no repository. Qualifiers split on white
// space, so adjacent repo: names are all returned.
func searchTargets(q string) ([]string, bool) {
	var names []string
	for _, word := range strings.Fields(q) {
		if name, ok := strings.CutPrefix(word, "repo:"); ok && repoName.MatchString(name) {
			names = append(names, name)
		}
	}
	return names, len(names) > 0
}

var repoName = regexp.MustCompile(`^[\w.-]+/[\w.-]+$`)

// publicReadTargets returns the repositories of a read on github.com that can
// run outside the room: a plain REST read, a list or view command with an
// explicit repository, or a search limited to named repositories. It asks no one.
func publicReadTargets(r repository, args []string) ([]string, bool) {
	// A command that names no repository by flag has no host yet; gh then uses
	// github.com unless it is told otherwise.
	if (r.host != "" && r.host != "github.com") || len(args) < 2 || slices.ContainsFunc(args, func(a string) bool { return a == "--hostname" || strings.HasPrefix(a, "--hostname=") }) {
		return nil, false
	}
	switch {
	case args[0] == "api" && isRead(args):
		if names, ok := apiSearchTargets(args); ok {
			return names, true
		}
		name, ok := publicReadTarget(args)
		if !ok {
			return nil, false
		}
		return []string{name}, true
	}
	return nil, false
}

// apiSearchTargets returns the repositories named by repo: qualifiers of a
// `gh api search/...` query, or false when the command is not such a search or
// a query names no repository.
func apiSearchTargets(args []string) ([]string, bool) {
	search := false
	var names []string
	for i := 1; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(strings.TrimPrefix(a, "/"), "search/") {
			search = true
			if _, q, ok := strings.Cut(a, "?"); ok {
				a = q
				if u, err := url.QueryUnescape(q); err == nil {
					a = u
				}
			}
		}
		for _, v := range []string{a, argValue(args, i)} {
			if _, q, ok := strings.Cut(v, "q="); ok {
				if found, ok := searchTargets(q); ok {
					names = append(names, found...)
				}
			}
		}
	}
	return names, search && len(names) > 0
}

// argValue returns the value following a field flag at position i, if any.
func argValue(args []string, i int) string {
	switch args[i] {
	case "-f", "-F", "--field", "--raw-field":
		if i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// publicReadTarget returns the repository of a plain REST read: the first
// form of read outside the room that can qualify.
func publicReadTarget(args []string) (string, bool) {
	var endpoint []string
	valueFlags := map[string]bool{"-X": true, "--method": true, "-q": true, "--jq": true, "-t": true, "--template": true, "-H": true, "--header": true, "--cache": true}
	for i := 1; i < len(args); i++ {
		switch {
		case valueFlags[args[i]]:
			i++
		case strings.HasPrefix(args[i], "-"):
			if args[i] == "--hostname" || strings.HasPrefix(args[i], "--hostname=") {
				return "", false
			}
		default:
			endpoint = append(endpoint, args[i])
		}
	}
	if len(endpoint) != 1 {
		return "", false
	}
	parts := strings.Split(strings.TrimPrefix(endpoint[0], "/"), "/")
	if len(parts) < 3 || parts[0] != "repos" || parts[1] == "" || parts[2] == "" {
		return "", false
	}
	return parts[1] + "/" + parts[2], true
}

// fetchPublicRepo asks GitHub, without credentials and on every call, whether a
// repository is public now, so a repository that turns private stops qualifying.
func fetchPublicRepo(ctx context.Context, r repository) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+r.name, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var body struct {
		Private bool `json:"private"`
	}
	return resp.StatusCode == http.StatusOK && json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body) == nil && !body.Private
}

func checkWorkerPolicy(args []string) error {
	args = commandArgs(args)
	if isRead(args) {
		return nil
	}
	if len(args) == 1 && args[0] == "status" {
		return nil
	}
	if len(args) >= 3 {
		switch args[0] + " " + args[1] + " " + args[2] {
		case "issue artifact download", "issue artifact list", "issue artifact view",
			"repo autolink list", "repo autolink view", "repo deploy-key list",
			"repo gitignore list", "repo gitignore view", "repo license list", "repo license view":
			return nil
		}
	}
	if len(args) >= 2 {
		switch args[0] + " " + args[1] {
		case "pr create", "pr edit", "pr ready",
			"agent-task list", "agent-task view",
			"attestation download", "attestation trusted-root", "attestation verify",
			"cache list", "codespace list", "codespace logs", "codespace ports", "codespace view",
			"discussion list", "discussion view", "extension browse", "extension list", "extension search",
			"gist clone", "gist list", "gist view", "gpg-key list", "issue status", "label list", "org list",
			"pr checkout", "pr status", "project field-list", "project item-list", "project list", "project view",
			"release download", "release list", "release verify", "release verify-asset", "release view",
			"repo clone", "repo list", "repo read-dir", "repo read-file",
			"ruleset check", "ruleset list", "ruleset view", "run watch",
			"search code", "search commits", "search issues", "search prs", "search repos",
			"secret list", "skill list", "skill preview", "skill search", "ssh-key list",
			"variable get", "variable list", "workflow list", "workflow view":
			return nil
		}
	}
	return errAgentGitHubWrite
}

func checkPolicy(ctx context.Context, cmdDir string, r repository, args []string) error {
	if len(args) == 1 && args[0] == "--version" {
		return nil
	}
	rec, err := roomFor(ctx, cmdDir, r)
	if err != nil {
		return err
	}
	if Access(ctx, rec) != ReadWrite && !isRead(args) {
		return errors.New("GitHub writes are disabled: this room is read-only")
	}
	return nil
}
