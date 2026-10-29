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
	ItemIssue ItemKind = "issue"
	ItemPR    ItemKind = "pr"
	ItemRun   ItemKind = "run"
)

// ItemRef names the issue, pull request or run whose author a lookup returns.
// Target is what the command gave: a number, a URL or a branch. For a pull
// request it is empty when the command uses the checked-out branch.
type ItemRef struct {
	Kind   ItemKind
	Host   string
	Repo   string
	Target string
}

// ClassifyInput carries what Classify needs besides the arguments.
type ClassifyInput struct {
	// Host and Repo name the target repository, as checkPolicy resolves it.
	Host string
	Repo string
	// Upstream is the room's recorded upstream, as host/owner/repo.
	Upstream string
	// Login is the login of the room's GitHub account.
	Login string
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
// It reads flags as repositoryFor does. Arguments after "--" are not read.
func parseArgs(command, args []string) parsedArgs {
	p := parsedArgs{flags: map[string]string{}}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
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
	authored := func(kind ItemKind, targets []string) bool {
		if in.Login == "" || in.AuthorOf == nil || len(targets) > 1 || (len(targets) == 0 && kind != ItemPR) {
			return false
		}
		ref := ItemRef{Kind: kind, Host: in.Host, Repo: in.Repo}
		if len(targets) == 1 {
			ref.Target = targets[0]
		}
		author, err := in.AuthorOf(ctx, ref)
		return err == nil && strings.EqualFold(author, in.Login)
	}
	byAuthor := func(kind ItemKind, targets []string) approvals.Class {
		if authored(kind, targets) {
			return approvals.ClassAgent
		}
		return approvals.ClassOwner
	}

	if args[0] == "api" {
		return classifyAPI(args, authored)
	}
	if len(args) < 2 {
		return approvals.ClassOwner
	}
	p := parseArgs(args[:2], args[2:])
	switch args[0] + " " + args[1] {
	case "issue create", "issue edit", "issue comment", "pr create", "pr edit", "pr ready", "pr comment":
		return approvals.ClassAgent
	case "pr review":
		_, comment := p.flags["--comment"]
		_, shortComment := p.flags["-c"]
		_, approve := p.flags["--approve"]
		_, shortApprove := p.flags["-a"]
		_, request := p.flags["--request-changes"]
		_, shortRequest := p.flags["-r"]
		if (comment || shortComment) && !approve && !shortApprove && !request && !shortRequest {
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
func classifyAPI(args []string, authored func(ItemKind, []string) bool) approvals.Class {
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
	if len(parts) < 4 || parts[0] != "repos" || (method != "POST" && method != "PATCH") {
		return approvals.ClassOwner
	}
	rest := parts[3:]
	last := rest[len(rest)-1]
	_, number := strconv.Atoi(rest[len(rest)-1])
	switch {
	case method == "POST" && last == "comments" && slices.Contains([]string{"issues", "pulls", "commits"}, rest[0]):
		return approvals.ClassAgent
	case method == "PATCH" && len(rest) >= 2 && len(rest) <= 3 && rest[len(rest)-2] == "comments" && slices.Contains([]string{"issues", "pulls", "comments"}, rest[0]):
		return approvals.ClassAgent
	case method == "POST" && len(rest) == 1 && (rest[0] == "issues" || rest[0] == "pulls"):
		return approvals.ClassAgent
	case method == "PATCH" && len(rest) == 2 && number == nil && (rest[0] == "issues" || rest[0] == "pulls"):
		kind := ItemIssue
		if rest[0] == "pulls" {
			kind = ItemPR
		}
		changesState := call.input || slices.ContainsFunc(call.keys, func(key string) bool { return strings.HasPrefix(key, "state") })
		if !changesState || authored(kind, rest[1:]) {
			return approvals.ClassAgent
		}
	}
	return approvals.ClassOwner
}
