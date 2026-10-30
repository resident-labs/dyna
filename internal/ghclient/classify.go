package ghclient

import (
	"strings"
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
