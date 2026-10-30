package ghclient

import (
	"context"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"dyna-agent/internal/approvals"
)

// ItemKind names what an ItemRef points at.
type ItemKind string

const (
	ItemIssue   ItemKind = "issue"
	ItemPR      ItemKind = "pr"
	ItemRun     ItemKind = "run"
	ItemComment ItemKind = "comment"
)

// ItemRef names the issue, pull request, run or comment whose author a lookup
// returns. Target is what the command gave. For a pull request it is empty when
// the command uses the checked-out branch. For a comment it is the endpoint
// path below the repository, such as issues/comments/7.
type ItemRef struct {
	Kind   ItemKind
	Host   string
	Repo   string
	Target string
}

// ClassifyInput carries what Classify needs besides the arguments.
type ClassifyInput struct {
	RoomRepository bool
	OwnPR          func(context.Context, ItemRef) bool
	// Host and Repo name the target repository, as checkPolicy resolves it.
	Host string
	Repo string
	// Upstream is the room's recorded upstream, as host/owner/repo.
	Upstream string
	// Login is the login of the room's GitHub account.
	Login string
	// APIHost is the host that gh api uses without --hostname: GH_HOST or gh's
	// configured default. It can differ from the checkout's host. When empty, a
	// REST state change cannot be bound to a host and gives owner.
	APIHost string
	// AuthorOf returns the login that authored an item. The engine supplies the
	// real lookup. An error, or a nil function, makes the class owner.
	AuthorOf func(ctx context.Context, ref ItemRef) (string, error)
}

// isReadCommand reports whether args are a gh read: a command that never
// changes anything on GitHub and runs without approval.
func isReadCommand(args []string) bool {
	if isRead(args) {
		return true
	}
	if len(args) == 1 && args[0] == "status" {
		return true
	}
	// --show-token prints the credential, so it is not a read.
	if len(args) >= 2 && args[0] == "auth" && args[1] == "status" {
		flags := parseArgs(args[:2], args[2:]).flags
		_, long := flags["--show-token"]
		_, short := flags["-t"]
		return !long && !short
	}
	if len(args) >= 2 && args[0] == "codespace" && args[1] == "ports" && codespacePortsListFlags(args[2:]) {
		return true
	}
	if len(args) >= 3 {
		switch args[0] + " " + args[1] + " " + args[2] {
		case "issue artifact download", "issue artifact list", "issue artifact view",
			"repo autolink list", "repo autolink view", "repo deploy-key list",
			"repo gitignore list", "repo gitignore view", "repo license list", "repo license view":
			return true
		}
	}
	if len(args) >= 2 {
		switch args[0] + " " + args[1] {
		case "agent-task list", "agent-task view",
			"attestation download", "attestation trusted-root", "attestation verify",
			"cache list", "codespace list", "codespace logs", "codespace view",
			"discussion list", "discussion view", "extension browse", "extension list", "extension search",
			"gist clone", "gist list", "gist view", "gpg-key list", "issue status", "label list", "org list",
			"pr checkout", "pr status", "project field-list", "project item-list", "project list", "project view",
			"release download", "release list", "release verify", "release verify-asset", "release view",
			"repo clone", "repo list", "repo read-dir", "repo read-file",
			"ruleset check", "ruleset list", "ruleset view", "run watch",
			"search code", "search commits", "search issues", "search prs", "search repos",
			"secret list", "skill list", "skill preview", "skill search", "ssh-key list",
			"variable get", "variable list", "workflow list", "workflow view":
			return true
		}
	}
	return false
}

// parsedArgs is the positional arguments and the flags of a gh command.
type parsedArgs struct {
	positional []string
	// flags maps each flag, as "--name" or "-x", to its value. Switches have "".
	flags map[string]string
}

// parseArgs splits the arguments that follow the command words. command is the
// command words, so repositoryFlagTakesValue knows which flags consume a value.
// Arguments after "--" are positional.
func parseArgs(command, args []string) parsedArgs {
	p := parsedArgs{flags: map[string]string{}}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			p.positional = append(p.positional, args[i+1:]...)
			return p
		case !strings.HasPrefix(arg, "-") || arg == "-":
			p.positional = append(p.positional, arg)
		case strings.HasPrefix(arg, "--"):
			flag, value, attached := strings.Cut(arg, "=")
			takes := flag == "--repo" || flag == "--hostname" || repositoryFlagTakesValue(command, flag)
			if !attached && takes && i+1 < len(args) {
				i++
				value = args[i]
			}
			p.flags[flag] = value
		default:
			for j := 1; j < len(arg); j++ {
				flag := "-" + arg[j:j+1]
				if arg[j] != 'R' && !repositoryFlagTakesValue(command, flag) {
					p.flags[flag] = ""
					continue
				}
				value := strings.TrimPrefix(arg[j+1:], "=")
				if j+1 == len(arg) && i+1 < len(args) {
					i++
					value = args[i]
				}
				p.flags[flag] = value
				break
			}
		}
	}
	return p
}

// commentOnlyReview reports whether a pr review only comments. An approval or
// a change request carries a verdict, so it is not routine collaboration.
func commentOnlyReview(p parsedArgs) bool {
	_, comment := p.flags["--comment"]
	_, shortComment := p.flags["-c"]
	_, approve := p.flags["--approve"]
	_, shortApprove := p.flags["-a"]
	_, request := p.flags["--request-changes"]
	_, shortRequest := p.flags["-r"]
	return (comment || shortComment) && !approve && !shortApprove && !request && !shortRequest
}

// Classify says who may approve a gh command. Reads are never held. A write is
// agent class when it is a routine collaboration step, and owner class in every
// other case, including any command it does not know.
func Classify(ctx context.Context, args []string, in ClassifyInput) approvals.Class {
	args = commandArgs(args)
	if isReadCommand(args) {
		return approvals.ClassRead
	}
	if len(args) == 0 {
		return approvals.ClassOwner
	}
	// authored reports whether the room's account authored the one item that a
	// state change names. A missing target, several targets or a failed lookup
	// give false.
	authored := func(ref ItemRef, targets []string) bool {
		if in.Login == "" || in.AuthorOf == nil || len(targets) > 1 || ref.Host == "" || (len(targets) == 0 && ref.Kind != ItemPR && ref.Kind != ItemComment) {
			return false
		}
		if len(targets) == 1 {
			ref.Target = targets[0]
		}
		author, err := in.AuthorOf(ctx, ref)
		return err == nil && strings.EqualFold(author, in.Login)
	}
	byAuthor := func(kind ItemKind, targets []string) approvals.Class {
		if authored(ItemRef{Kind: kind, Host: in.Host, Repo: in.Repo}, targets) {
			return approvals.ClassAgent
		}
		return approvals.ClassOwner
	}

	if args[0] == "api" {
		return classifyAPI(args, in.APIHost, authored)
	}
	if len(args) < 2 {
		return approvals.ClassOwner
	}
	p := parseArgs(args[:2], args[2:])
	switch args[0] + " " + args[1] {
	case "pr create":
		if in.RoomRepository {
			return approvals.ClassAuto
		}
		return approvals.ClassAgent
	case "pr edit", "pr ready", "pr comment":
		if in.RoomRepository && in.OwnPR != nil && len(p.positional) <= 1 {
			ref := ItemRef{Kind: ItemPR, Host: in.Host, Repo: in.Repo}
			if len(p.positional) == 1 {
				ref.Target = p.positional[0]
			}
			if in.OwnPR(ctx, ref) {
				return approvals.ClassAuto
			}
		}
		return approvals.ClassAgent
	case "issue create", "issue edit", "issue comment":
		return approvals.ClassAgent
	case "pr review":
		if commentOnlyReview(p) {
			return approvals.ClassAgent
		}
	case "issue close", "issue reopen":
		return byAuthor(ItemIssue, p.positional)
	case "pr close", "pr reopen":
		return byAuthor(ItemPR, p.positional)
	case "run rerun", "run cancel":
		return byAuthor(ItemRun, p.positional)
	case "repo fork":
		if _, org := p.flags["--org"]; org || in.Upstream == "" {
			return approvals.ClassOwner
		}
		target := in.Host + "/" + in.Repo
		if len(p.positional) > 1 {
			return approvals.ClassOwner
		}
		if len(p.positional) == 1 {
			// gh takes OWNER/REPO, HOST/OWNER/REPO or a URL.
			arg := p.positional[0]
			if strings.HasPrefix(arg, "https://") || strings.HasPrefix(arg, "http://") {
				u, err := url.Parse(arg)
				if err != nil {
					return approvals.ClassOwner
				}
				target = u.Hostname() + "/" + strings.Trim(u.Path, "/")
			} else if strings.Count(arg, "/") == 1 {
				target = "github.com/" + arg
			} else {
				target = arg
			}
			target = strings.TrimSuffix(target, ".git")
		}
		if strings.EqualFold(target, in.Upstream) {
			return approvals.ClassAgent
		}
	}
	return approvals.ClassOwner
}

// classifyAPI classifies a gh api write: REST POST and PATCH to a repository's
// issues, pulls and comments. Closing or reopening an item needs authorship.
func classifyAPI(args []string, host string, authored func(ItemRef, []string) bool) approvals.Class {
	call := parseAPICall(args)
	p := parseArgs(args[:1], args[1:])
	if call.graphql || len(p.positional) != 1 {
		return approvals.ClassOwner
	}
	method := call.method
	if method == "" && call.fields {
		method = "POST"
	}
	endpoint, _, _ := strings.Cut(p.positional[0], "?")
	parts := strings.Split(strings.Trim(endpoint, "/"), "/")
	if len(parts) < 4 || parts[0] != "repos" {
		return approvals.ClassOwner
	}
	// The lookup binds to the repository the endpoint names, not the checkout's.
	if h := p.flags["--hostname"]; h != "" {
		host = h
	}
	ref := ItemRef{Host: host, Repo: parts[1] + "/" + parts[2]}
	if comment, ok := ownCommentCall(args); ok && comment.repo == ref.Repo {
		if comment.method == "PATCH" && (call.input || !slices.Contains(call.keys, "body")) {
			return approvals.ClassOwner
		}
		ref.Kind, ref.Target = ItemComment, comment.kind+"/comments/"+comment.id
		if authored(ref, nil) {
			return approvals.ClassAgent
		}
		return approvals.ClassOwner
	}
	if method != "POST" && method != "PATCH" {
		return approvals.ClassOwner
	}
	rest := parts[3:]
	last := rest[len(rest)-1]
	_, number := strconv.Atoi(rest[len(rest)-1])
	switch {
	case method == "POST" && last == "comments" && slices.Contains([]string{"issues", "pulls", "commits"}, rest[0]):
		return approvals.ClassAgent
	case method == "POST" && len(rest) == 1 && (rest[0] == "issues" || rest[0] == "pulls"):
		return approvals.ClassAgent
	case method == "POST" && len(rest) == 3 && rest[0] == "pulls" && rest[2] == "reviews" && call.event == "COMMENT" && !call.input && !slices.ContainsFunc(call.keys, func(key string) bool { return strings.HasPrefix(key, "comments") }):
		// A comment-only review with no inline comments, nested comments[] fields included.
		// Its commit_id pins the reviewed head.
		if _, err := strconv.Atoi(rest[1]); err == nil {
			return approvals.ClassAgent
		}
	case method == "PATCH" && len(rest) == 2 && number == nil && (rest[0] == "issues" || rest[0] == "pulls"):
		ref.Kind = ItemIssue
		if rest[0] == "pulls" {
			ref.Kind = ItemPR
		}
		changesState := call.input || slices.ContainsFunc(call.keys, func(key string) bool { return strings.HasPrefix(key, "state") })
		if !changesState || authored(ref, rest[1:]) {
			return approvals.ClassAgent
		}
	}
	return approvals.ClassOwner
}
