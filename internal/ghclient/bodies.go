package ghclient

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"

	"dyna-agent/internal/account/store"
)

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

// publicText returns the title and body that a create or edit command would
// publish. A body read from standard input is read here and put back.
func publicText(cmd *exec.Cmd) (string, error) {
	args := commandArgs(cmd.Args[1:])
	if len(args) < 2 || (args[0] != "issue" && args[0] != "pr") || (args[1] != "create" && args[1] != "edit") {
		return "", nil
	}
	var text strings.Builder
	for i := 2; i < len(args); i++ {
		name, value, attached := strings.Cut(args[i], "=")
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
func refuseOwnerIdentity(cmd *exec.Cmd) error {
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
