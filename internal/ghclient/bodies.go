package ghclient

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

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

var errUninspectableText = errors.New("this command builds its title or body from commits, a template, an editor or the browser, so Dyna cannot check it; pass --title and --body-file instead")

// publicText returns the title and body that a create or edit command would
// publish. A body read from standard input is read here and put back.
func publicText(cmd *exec.Cmd) (string, error) {
	args := commandArgs(cmd.Args[1:])
	if len(args) < 2 || (args[0] != "issue" && args[0] != "pr") || (args[1] != "create" && args[1] != "edit") {
		return "", nil
	}
	var text strings.Builder
	for i := 2; i < len(args); i++ {
		if args[i] == "--" {
			break
		}
		name, value, attached := strings.Cut(args[i], "=")
		if len(args[i]) > 2 && args[i][0] == '-' && args[i][1] != '-' {
			// A short flag may carry its value: -bTEXT or -b=TEXT.
			name, value, attached = args[i][:2], strings.TrimPrefix(args[i][2:], "="), true
		}
		if generatedTextFlags[name] {
			return "", errUninspectableText
		}
		isFile, known := textFlags[name]
		if !known {
			if createValueFlags[name] && !attached {
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
		switch {
		case !isFile:
		case value == "-":
			if cmd.Stdin == nil {
				continue
			}
			data, err := io.ReadAll(cmd.Stdin)
			if err != nil {
				return "", err
			}
			cmd.Stdin = bytes.NewReader(data)
			value = string(data)
		default:
			if !filepath.IsAbs(value) {
				value = filepath.Join(cmd.Dir, value)
			}
			data, err := os.ReadFile(value)
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

// withAttribution ends the body that an issue or pull request create or edit
// publishes with the room's attribution line, once. A create without a body
// gets the line as its body; an edit without one keeps the published body.
// The returned function removes the temporary body file, if any.
func withAttribution(ctx context.Context, cmd *exec.Cmd, r repository) (func(), error) {
	none := func() {}
	if !isAgentText(ctx) {
		return none, nil
	}
	args := cmd.Args[1:]
	command := commandArgs(args)
	if len(command) < 2 || (command[0] != "issue" && command[0] != "pr") || (command[1] != "create" && command[1] != "edit") {
		return none, nil
	}
	rec, err := roomFor(ctx, cmd.Dir, r)
	if err != nil && !errors.Is(err, errNotInRoom) {
		return none, nil
	}
	if rec.Attribution == "" {
		return none, nil
	}
	start := len(args) - len(command) + 2
	body, rest := "", append([]string(nil), args[:start]...)
	found := false
	for i := start; i < len(args); i++ {
		name, value, attached := strings.Cut(args[i], "=")
		if len(args[i]) > 2 && args[i][0] == '-' && args[i][1] != '-' {
			name, value, attached = args[i][:2], strings.TrimPrefix(args[i][2:], "="), true
		}
		isFile, text := textFlags[name]
		if !text || name == "--title" || name == "-t" {
			rest = append(rest, args[i])
			if createValueFlags[name] && !attached && i+1 < len(args) {
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
		switch {
		case !isFile:
			body = value
		case value == "-":
			if cmd.Stdin == nil {
				continue
			}
			data, err := io.ReadAll(cmd.Stdin)
			if err != nil {
				return none, err
			}
			body = string(data)
		default:
			if !filepath.IsAbs(value) {
				value = filepath.Join(cmd.Dir, value)
			}
			data, err := os.ReadFile(value)
			if err != nil {
				return none, err
			}
			body = string(data)
		}
	}
	if !found && command[1] == "edit" {
		return none, nil
	}
	body = strings.TrimRight(body, "\n")
	if !strings.HasSuffix(body, rec.Attribution) {
		if body != "" {
			body += "\n\n"
		}
		body += rec.Attribution
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
	cmd.Args = append([]string{cmd.Args[0]}, append(rest, "--body-file="+file.Name())...)
	return cleanup, nil
}
