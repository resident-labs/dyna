package ghclient

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"unicode"

	"dyna-agent/internal/account/store"
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
	"issue edit": false, "pr edit": false, "issue comment": false, "pr comment": false, "pr review": false,
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

// publicText returns the title and body that a create or edit command would
// publish. A body read from standard input is read here and put back.
func publicText(cmd *exec.Cmd) (string, error) {
	args := commandArgs(cmd.Args[1:])
	if _, ok := ownCommentCall(args); ok {
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
	var text strings.Builder
	for i := 2; i < len(args); i++ {
		if args[i] == "--" {
			break
		}
		name, value, attached := strings.Cut(args[i], "=")
		if flag, rest, ok := shortFlag(args[i]); ok {
			name, value, attached = flag, strings.TrimPrefix(rest, "="), true
		}
		if generatedTextFlags[name] {
			return "", errUninspectableText
		}
		isFile, known := textFlags[name]
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

// refuseOwnerIdentity refuses a title or body that contains a saved sign-in
// identity of the owner (D-501). Issues and pull requests are often public.
func refuseOwnerIdentity(ctx context.Context, cmd *exec.Cmd) error {
	if !IsAgentText(ctx) {
		return nil
	}
	text, err := publicText(cmd)
	if err != nil || text == "" {
		return err
	}
	identities, err := ownerIdentities()
	if err != nil {
		return err
	}
	lower := strings.ToLower(text)
	for _, identity := range identities {
		if strings.Contains(lower, strings.ToLower(identity)) {
			return errOwnerIdentityInBody
		}
	}
	return nil
}

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

// withAttributionFirst adds the room line unless the body already has its marker.
// Slash commands keep their start so that comment bots can recognize them.
func withAttributionFirst(body, line string) string {
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
	if command, ok := strings.CutPrefix(first, "/"); ok {
		for _, r := range command {
			if unicode.IsLetter(r) {
				return strings.TrimRight(body, "\n") + "\n\n" + line
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

// withAttribution starts the body that an agent's issue or pull request create,
// edit or comment publishes with the room's attribution line, once. A create
// without a body gets the line as its body; any other command without a body
// keeps the published text. The returned function removes the temporary body
// file, if any.
func withAttribution(ctx context.Context, cmd *exec.Cmd, r repository) (func(), error) {
	none := func() {}
	if !IsAgentText(ctx) {
		return none, nil
	}
	args := cmd.Args[1:]
	command := commandArgs(args)
	_, comment := ownCommentCall(command)
	publishesAlone, isText := false, false
	if len(command) >= 2 {
		publishesAlone, isText = bodyCommands[command[0]+" "+command[1]]
	}
	if !isText && !comment {
		return none, nil
	}
	rec, err := roomFor(ctx, cmd.Dir, r)
	if err != nil && !errors.Is(err, errNotInRoom) {
		return none, nil
	}
	if rec.Attribution == "" {
		return none, nil
	}
	if comment {
		// A comment edit changes only its body field; a DELETE has none.
		rest, body, found, err := apiBody(cmd, command)
		if err != nil || !found {
			return none, err
		}
		cmd.Args = append(append([]string{cmd.Args[0]}, args[:len(args)-len(command)]...), append(rest, "--raw-field", "body="+withAttributionFirst(body, rec.Attribution))...)
		return none, nil
	}
	valueFlags := createValueFlags
	if command[1] != "create" && command[1] != "edit" {
		valueFlags = nil
	}
	start := len(args) - len(command) + 2
	body, rest := "", append([]string(nil), args[:start]...)
	found := false
	for i := start; i < len(args); i++ {
		name, value, attached := strings.Cut(args[i], "=")
		if flag, rest, ok := shortFlag(args[i]); ok {
			name, value, attached = flag, strings.TrimPrefix(rest, "="), true
		}
		isFile, text := textFlags[name]
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
	body = withAttributionFirst(body, rec.Attribution)
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
	cmd.Args = append([]string{cmd.Args[0]}, append(rest, "--body-file="+file.Name())...)
	return cleanup, nil
}
