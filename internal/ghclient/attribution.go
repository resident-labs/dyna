package ghclient

import (
	"encoding/json"
	"strings"

	"dyna-agent/internal/approvals"
)

// AttributeApproval adds the room line to comment and review text before the
// payload is validated, displayed and stored. Create and edit use bodies.go.
func AttributeApproval(payload approvals.GHPayload, line string) approvals.GHPayload {
	payload.Argv = append([]string(nil), payload.Argv...)
	payload.Files = append([]approvals.FileSnapshot(nil), payload.Files...)
	if line == "" {
		return payload
	}
	command := commandArgs(payload.Argv)
	if len(command) < 2 {
		return payload
	}
	api := command[0] == "api" && isCommentAPI(command)
	closeComment := (command[0] == "issue" || command[0] == "pr") && command[1] == "close"
	comment := (command[0] == "issue" || command[0] == "pr") && (command[1] == "comment" || command[1] == "review")
	if !api && !closeComment && !comment {
		return payload
	}
	refs, err := approvalFileArguments(command)
	if err != nil {
		return payload
	}
	for _, ref := range refs {
		if ref.name != "body" && !(closeComment && ref.name == "comment") && !(api && ref.name == "") {
			continue
		}
		if !ref.file {
			command[ref.index] = ref.prefix + attributeText(ref.value, line)
			continue
		}
		for i := range payload.Files {
			if payload.Files[i].Path != ref.path {
				continue
			}
			if api && ref.name == "" {
				payload.Files[i].Data = attributeAPIInput(payload.Files[i].Data, line)
			} else {
				payload.Files[i].Data = []byte(attributeText(string(payload.Files[i].Data), line))
			}
		}
	}
	return payload
}

func attributeText(body, line string) string {
	body = strings.TrimRight(body, "\n")
	if strings.HasSuffix(body, line) {
		return body
	}
	if body != "" {
		body += "\n\n"
	}
	return body + line
}

func attributeAPIInput(data []byte, line string) []byte {
	var object map[string]json.RawMessage
	if json.Unmarshal(data, &object) != nil || object == nil {
		return data
	}
	body := ""
	if raw := object["body"]; raw != nil && json.Unmarshal(raw, &body) != nil {
		return data
	}
	encoded, _ := json.Marshal(attributeText(body, line))
	object["body"] = encoded
	out, err := json.Marshal(object)
	if err != nil {
		return data
	}
	return out
}

func isCommentAPI(args []string) bool {
	call := parseAPICall(args)
	if call.graphql {
		return false
	}
	method := call.method
	if method == "" && call.fields {
		method = "POST"
	}
	if method != "POST" && method != "PATCH" {
		return false
	}
	parsed := parseArgs(args[:1], args[1:])
	if len(parsed.positional) != 1 {
		return false
	}
	endpoint, _, _ := strings.Cut(parsed.positional[0], "?")
	parts := strings.Split(strings.Trim(endpoint, "/"), "/")
	if len(parts) < 4 || parts[0] != "repos" {
		return false
	}
	for _, part := range parts[3:] {
		if part == "comments" || part == "reviews" {
			return true
		}
	}
	return false
}
