package ghclient

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"unicode"

	"dyna-agent/internal/account/store"
	"dyna-agent/internal/room"
)

type agentTextKey struct{}

// WithAgentText marks a context whose gh commands carry text an agent wrote.
// Only the agent's gh shim sets it. Dyna's own calls, such as the owner's PR
// edit, carry the owner's text and are never checked or changed.
func WithAgentText(ctx context.Context) context.Context {
	return context.WithValue(ctx, agentTextKey{}, true)
}

// IsAgentText reports whether ctx was marked by WithAgentText.
func IsAgentText(ctx context.Context) bool {
	agent, _ := ctx.Value(agentTextKey{}).(bool)
	return agent
}

// errOwnerIdentityInBody refuses text that names a person behind a saved sign-in.
// The message never repeats the matched text, so a refusal cannot leak it again.
var errOwnerIdentityInBody = errors.New("the title or body names the owner's sign-in account (a name or an email saved in Dyna accounts); remove it, or use the room's attribution line instead, and run the command again")

// minIdentityLen keeps short labels such as "work" from matching ordinary words.
const minIdentityLen = 6

// ownerIdentities lists the identities and labels of the saved personal
// sign-ins. Tests replace it.
var ownerIdentities = func() ([]string, error) {
	state, err := store.DefaultStore().Read()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, entry := range state.Entries {
		if entry.Kind != store.KindOAuth {
			continue
		}
		for _, value := range []string{entry.Identity, entry.Label} {
			if value = strings.TrimSpace(value); len(value) >= minIdentityLen {
				out = append(out, value)
			}
		}
	}
	return out, nil
}

// textFlags are the gh flags whose values become public text, and which of
// them name a file instead of holding the text.
var textFlags = map[string]bool{"--title": false, "-t": false, "--body": false, "-b": false, "--body-file": true, "-F": true}

// closeTextFlags are the comment flags of issue close and reopen. On pr review
// -c and --comment are switches, so they stay out of textFlags.
var closeTextFlags = map[string]bool{"--comment": false, "-c": false}

// closesIssue reports whether command is gh issue close or reopen, whose comment
// flag takes the text itself and no file.
func closesIssue(command []string) bool {
	return len(command) >= 2 && command[0] == "issue" && (command[1] == "close" || command[1] == "reopen")
}

// bodyFlagsFor returns the text flags of a command.
func bodyFlagsFor(command []string) map[string]bool {
	if closesIssue(command) {
		return closeTextFlags
	}
	return textFlags
}

// generatedTextFlags make gh build the title or body from commits, a template
// or an editor, so the text cannot be read before gh publishes it.
var generatedTextFlags = map[string]bool{
	"--fill": true, "-f": true, "--fill-first": true, "--fill-verbose": true,
	"--template": true, "-T": true, "--recover": true, "--attach": true, "--web": true, "-w": true, "--editor": true, "-e": true,
}

// bodyCommands are the gh commands that publish a body, and whether the command
// publishes one even when it names none: a create without a body gets the
// attribution line alone, a comment or an edit without a body stays as it is.
var bodyCommands = map[string]bool{
	"issue create": true, "pr create": true,
	"issue edit": false, "pr edit": false, "issue comment": false, "issue close": false, "issue reopen": false, "pr comment": false, "pr review": false,
}

// shortFlag splits a short flag that carries its value, such as -bTEXT or
// -b=TEXT. It returns the flag, the raw text after it, and whether arg is such
// a flag. Callers decide whether a leading equals sign is part of the value.
func shortFlag(arg string) (flag, rest string, ok bool) {
	if len(arg) > 2 && arg[0] == '-' && arg[1] != '-' {
		return arg[:2], arg[2:], true
	}
	return "", "", false
}

var errUninspectableText = errors.New("this command builds its title or body from commits, a template, an editor or the browser, so Dyna cannot check it; pass --title and --body-file instead")

// attributionMarker is the start of an attribution line, up to and including
// its first token that is not punctuation: "> 🤖" for "> 🤖 Opened by an agent."
// A body that already starts with the marker counts as attributed, so a room's
// own variants of the line are not doubled.
func attributionMarker(line string) string {
	isMark := func(r rune) bool {
		return unicode.IsPunct(r) || (r < unicode.MaxASCII && !unicode.IsLetter(r) && !unicode.IsDigit(r))
	}
	end := 0
	for _, loc := range nonSpace.FindAllStringIndex(line, -1) {
		end = loc[1]
		if strings.IndexFunc(line[loc[0]:loc[1]], func(r rune) bool { return !isMark(r) }) >= 0 {
			break
		}
	}
	return line[:end]
}

var nonSpace = regexp.MustCompile(`\S+`)
var apiReviewEndpoint = regexp.MustCompile(`^/?repos/[\w.-]+/[\w.-]+/pulls/[0-9]+/reviews$`)

// ComposeBody inspects agent text and applies the room attribution before publication.
func ComposeBody(ctx context.Context, cmd *exec.Cmd, r repository) (func(), error) {
	none := func() {}
	if !IsAgentText(ctx) {
		return none, nil
	}
	args := cmd.Args[1:]
	command := commandArgs(args)
	_, apiText := ownCommentCall(command)
	kind := room.AttributionComment
	if len(command) > 0 && command[0] == "api" {
		call := parseAPICall(command)
		positional := parseArgs(command[:1], command[1:]).positional
		if len(positional) == 1 && apiReviewEndpoint.MatchString(positional[0]) && (call.method == "POST" || call.method == "" && call.fields) {
			apiText = true
			kind = room.AttributionReview
		}
	}
	inspect := func() (string, error) {
		args := command
		if apiText {
			_, body, _, err := apiBody(cmd, args)
			return body, err
		}
		if len(args) < 2 {
			return "", nil
		}
		if _, ok := bodyCommands[args[0]+" "+args[1]]; !ok {
			return "", nil
		}
		valueFlags := createValueFlags
		if args[1] != "create" && args[1] != "edit" {
			// Comment and review flags such as -r and -a take no value.
			valueFlags = nil
		}
		flags := bodyFlagsFor(args)
		var text strings.Builder
		for i := 2; i < len(args); i++ {
			if args[i] == "--" {
				break
			}
			name, value, attached := strings.Cut(args[i], "=")
			if flag, rest, ok := shortFlag(args[i]); ok {
				name, value, attached = flag, strings.TrimPrefix(rest, "="), true
			}
			if name == "--attach" && args[0] == "pr" && (args[1] == "create" || args[1] == "comment") {
				if !attached {
					i++
				}
				continue
			}
			if generatedTextFlags[name] {
				return "", errUninspectableText
			}
			isFile, known := flags[name]
			if !known {
				if valueFlags[name] && !attached {
					i++
				}
				continue
			}
			if !attached {
				if i++; i >= len(args) {
					break
				}
				value = args[i]
			}
			if isFile {
				data, err := readBodyFile(cmd, value)
				if err != nil {
					return "", err
				}
				value = string(data)
			}
			text.WriteString(value)
			text.WriteByte('\n')
		}
		return text.String(), nil
	}

	text, err := inspect()
	if err != nil {
		return none, err
	}
	if text != "" {
		identities, err := ownerIdentities()
		if err != nil {
			return none, err
		}
		lower := strings.ToLower(text)
		for _, identity := range identities {
			if strings.Contains(lower, strings.ToLower(identity)) {
				return none, errOwnerIdentityInBody
			}
		}
	}
	publishesAlone, isText := false, false
	if len(command) >= 2 {
		publishesAlone, isText = bodyCommands[command[0]+" "+command[1]]
		switch command[0] + " " + command[1] {
		case "pr create", "pr edit":
			kind = room.AttributionPR
		case "issue create", "issue edit":
			kind = room.AttributionIssue
		case "pr review":
			kind = room.AttributionReview
		}
	}
	if !isText && !apiText {
		return none, nil
	}
	rec, err := roomFor(ctx, cmd.Dir, r)
	if err != nil && !errors.Is(err, errNotInRoom) {
		return none, err
	}
	a := room.Attribution{Text: rec.Attribution, Placement: rec.AttributionPlacement}
	if override, ok := rec.AttributionKinds[kind]; ok {
		a = override
	}
	if a.Text == "" {
		return none, nil
	}
	if strings.Contains(a.Text, "{login}") {
		login, _, err := selectAccountAfterPolicy(ctx, cmd, r)
		if err != nil {
			return none, fmt.Errorf("resolve attribution login: %w", err)
		}
		if login == "" {
			return none, errors.New("resolve attribution login: no account")
		}
		a.Text = strings.ReplaceAll(a.Text, "{login}", login)
	}
	placement := a.Placement
	format := func(body, line string) string {
		first := ""
		for _, candidate := range strings.Split(body, "\n") {
			if first = strings.TrimSpace(candidate); first != "" {
				break
			}
		}
		marker := strings.TrimLeft(attributionMarker(line), "> ")
		if strings.HasPrefix(strings.TrimLeft(first, "> "), marker) {
			return strings.Trim(body, "\n")
		}
		if placement == room.AttributionEnd {
			body = strings.Trim(body, "\n")
			if body == "" {
				return line
			}
			if strings.HasSuffix(body, "\n\n"+line) {
				return body
			}
			return body + "\n\n" + line
		}
		if placement == room.AttributionPrefix {
			body = strings.Trim(body, "\n")
			if body == "" {
				return line
			}
			return line + " " + body
		}
		if command, ok := strings.CutPrefix(first, "/"); ok {
			for _, r := range command {
				if unicode.IsLetter(r) {
					body = strings.TrimRight(body, "\n")
					if strings.HasSuffix(body, "\n\n"+line) {
						return body
					}
					return body + "\n\n" + line
				}
				break
			}
		}
		body = strings.Trim(body, "\n")
		if body == "" {
			return line
		}
		return line + "\n\n" + body
	}

	if apiText {
		// A comment edit changes only its body field; a DELETE has none.
		rest, body, found, err := apiBody(cmd, command)
		if err != nil || !found {
			return none, err
		}
		cmd.Args = append(append([]string{cmd.Args[0]}, args[:len(args)-len(command)]...), append(rest, "--raw-field", "body="+format(body, a.Text))...)
		return none, nil
	}
	valueFlags := createValueFlags
	if command[1] != "create" && command[1] != "edit" {
		valueFlags = nil
	}
	flags := bodyFlagsFor(command)
	start := len(args) - len(command) + 2
	body, rest := "", append([]string(nil), args[:start]...)
	found := false
	// Arguments after "--" are positional, so the attributed flag goes before them.
	var tail []string
	for i := start; i < len(args); i++ {
		if args[i] == "--" {
			tail = args[i:]
			break
		}
		name, value, attached := strings.Cut(args[i], "=")
		if flag, rest, ok := shortFlag(args[i]); ok {
			name, value, attached = flag, strings.TrimPrefix(rest, "="), true
		}
		isFile, text := flags[name]
		if !text || name == "--title" || name == "-t" {
			rest = append(rest, args[i])
			if valueFlags[name] && !attached && i+1 < len(args) {
				i++
				rest = append(rest, args[i])
			}
			continue
		}
		if !attached && i+1 < len(args) {
			i++
			value = args[i]
		}
		found = true
		body = value
		if isFile {
			data, err := readBodyFile(cmd, value)
			if err != nil {
				return none, err
			}
			body = string(data)
		}
	}
	if !found && !publishesAlone {
		return none, nil
	}
	body = format(body, a.Text)
	if closesIssue(command) {
		cmd.Args = append([]string{cmd.Args[0]}, append(append(rest, "--comment="+body), tail...)...)
		return none, nil
	}
	file, err := os.CreateTemp("", "dyna-gh-body-")
	if err != nil {
		return none, err
	}
	cleanup := func() { os.Remove(file.Name()) }
	_, werr := file.WriteString(body + "\n")
	if cerr := file.Close(); werr != nil || cerr != nil {
		cleanup()
		return none, errors.Join(werr, cerr)
	}
	cmd.Args = append([]string{cmd.Args[0]}, append(append(rest, "--body-file="+file.Name()), tail...)...)
	return cleanup, nil
}
