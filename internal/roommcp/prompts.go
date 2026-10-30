package roommcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"unicode"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/room"
	"dyna-agent/internal/slashcmd"
)

// Prompts lists pinned prompts on attached servers in the requested room.
func (s *Service) Prompts(ctx context.Context, roomRef string) ([]slashcmd.Command, error) {
	rec, err := s.rooms.Resolve(roomRef)
	if err != nil {
		return nil, err
	}
	if _, err = authority.ActorIn(ctx, rec.ID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT p.server,p.definition FROM room_mcp_prompts p JOIN room_mcp_servers s ON s.room=p.room AND s.name=p.server WHERE p.room=? AND s.attached=1 ORDER BY p.server,p.name`, rec.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []slashcmd.Command{}
	for rows.Next() {
		var server, definition string
		if err := rows.Scan(&server, &definition); err != nil {
			return nil, err
		}
		var p Prompt
		if err := json.Unmarshal([]byte(definition), &p); err != nil {
			return nil, err
		}
		cmd := slashcmd.Command{Name: server + ":" + p.Name, Source: slashcmd.MCPPrompt, Description: p.Description, Args: []slashcmd.Arg{}}
		for _, a := range p.Arguments {
			cmd.Args = append(cmd.Args, slashcmd.Arg{Name: a.Name, Description: a.Description, Required: a.Required})
		}
		out = append(out, cmd)
	}
	return out, rows.Err()
}

// splitPromptArgs splits named arguments while preserving quoted spaces and equals.
func splitPromptArgs(input string) ([]string, error) {
	var fields []string
	var field strings.Builder
	var quote rune
	escaped, started := false, false
	for _, ch := range input {
		switch {
		case escaped:
			field.WriteRune(ch)
			escaped = false
		case ch == '\\' && quote != '\'':
			escaped = true
			started = true
		case quote != 0 && ch == quote:
			quote = 0
		case quote != 0:
			field.WriteRune(ch)
		case ch == '\'' || ch == '"':
			quote = ch
			started = true
		case unicode.IsSpace(ch):
			if started {
				fields = append(fields, field.String())
				field.Reset()
				started = false
			}
		default:
			field.WriteRune(ch)
			started = true
		}
	}
	if escaped || quote != 0 {
		return nil, errors.New("invalid prompt arguments")
	}
	if started {
		fields = append(fields, field.String())
	}
	return fields, nil
}

// RunPrompt invokes a pinned prompt for the owner after scanning its arguments.
func (s *Service) RunPrompt(ctx context.Context, roomRef, name, text string) (string, error) {
	rec, err := s.manage(ctx, roomRef, true)
	if err != nil {
		return "", err
	}
	server, promptName, ok := strings.Cut(name, ":")
	if !ok || server == "" || promptName == "" {
		return "", errors.New("invalid prompt name")
	}
	key := serverKey{rec.ID, server}
	s.mu.Lock()
	generation := s.gen[key]
	var definition string
	err = s.db.QueryRowContext(ctx, `SELECT p.definition FROM room_mcp_prompts p JOIN room_mcp_servers s ON s.room=p.room AND s.name=p.server WHERE p.room=? AND p.server=? AND p.name=? AND s.attached=1`, rec.ID, server, promptName).Scan(&definition)
	s.mu.Unlock()
	if errors.Is(err, sql.ErrNoRows) {
		return "", errors.New("prompt is not pinned on an attached server")
	}
	if err != nil {
		return "", err
	}
	started := s.now()
	finish := func(outcome room.MCPOutcome, size int) {
		event := room.MCPCallEvent{Server: server, Tool: promptName, Outcome: outcome, ResultBytes: size, DurationMs: s.now().Sub(started).Milliseconds()}
		if _, err := s.rooms.PostEventInput(room.PostEventInput{RoomID: rec.ID, Event: event, To: []string{rec.Name}, FreshID: true}); err != nil {
			log.Printf("room MCP prompt audit was not written: %v", err)
		}
	}
	var prompt Prompt
	if err := json.Unmarshal([]byte(definition), &prompt); err != nil {
		finish(room.MCPServerError, 0)
		return "", err
	}
	args := map[string]string{}
	valid := map[string]bool{}
	required := []string{}
	for _, a := range prompt.Arguments {
		valid[a.Name] = true
		if a.Required {
			required = append(required, a.Name)
		}
	}
	text = strings.TrimSpace(text)
	first := strings.Fields(text)
	named := len(first) > 0 && strings.Contains(first[0], "=")
	if text != "" && len(required) == 1 && !named {
		args[required[0]] = text
	} else if text != "" {
		fields, parseErr := splitPromptArgs(text)
		if parseErr != nil {
			finish(room.MCPFlagged, 0)
			return "", parseErr
		}
		for _, field := range fields {
			key, value, ok := strings.Cut(field, "=")
			if !ok || !valid[key] || value == "" {
				finish(room.MCPFlagged, 0)
				return "", errors.New("invalid prompt arguments")
			}
			if _, exists := args[key]; exists {
				finish(room.MCPFlagged, 0)
				return "", errors.New("duplicate prompt argument")
			}
			args[key] = value
		}
	}
	for _, key := range required {
		if args[key] == "" {
			finish(room.MCPFlagged, 0)
			return "", errors.New("missing required prompt argument")
		}
	}
	raw, err := json.Marshal(args)
	if err != nil {
		finish(room.MCPFlagged, 0)
		return "", err
	}
	if err := s.scanCall(ctx, rec, server, raw); err != nil {
		finish(room.MCPFlagged, 0)
		return "", err
	}
	rt, err := s.connection(ctx, rec.ID, server)
	if err != nil {
		finish(room.MCPServerError, 0)
		return "", err
	}
	// The definition and connection must belong to the same server generation.
	// A refresh can replace definitions without changing the generation.
	s.mu.Lock()
	var current string
	checkErr := s.db.QueryRowContext(ctx, `SELECT p.definition FROM room_mcp_prompts p JOIN room_mcp_servers s ON s.room=p.room AND s.name=p.server WHERE p.room=? AND p.server=? AND p.name=? AND s.attached=1`, rec.ID, server, promptName).Scan(&current)
	changed := checkErr != nil || current != definition || s.gen[key] != generation || rt.gen != generation || rt.lifetime.Err() != nil
	s.mu.Unlock()
	if changed {
		finish(room.MCPDetached, 0)
		return "", errChanged
	}
	secrets := []string{rt.client.token}
	callCtx, stop := context.WithCancel(ctx)
	defer stop()
	defer context.AfterFunc(rt.lifetime, stop)()
	err = rt.initialize()
	handshakeFailed := err != nil
	var result PromptResult
	if err == nil {
		result, err = rt.client.GetPrompt(callCtx, promptName, args)
	}
	detached := rt.lifetime.Err() != nil
	if handshakeFailed {
		s.forget(serverKey{rec.ID, server}, rt)
	}
	var serverErr *ServerError
	output := Result{}
	switch {
	case errors.As(err, &serverErr):
		finish(room.MCPServerError, 0)
		return "", errors.New(Scrub(serverErr.Message, secrets))
	case err != nil && detached:
		finish(room.MCPDetached, 0)
		return "", errors.New("MCP server is detached")
	case err != nil:
		finish(room.MCPServerError, 0)
		return "", errors.New(Scrub(err.Error(), secrets))
	default:
		for _, message := range result.Messages {
			if message.Content.Type == "text" {
				output.Content = append(output.Content, Content{Type: "text", Text: message.Role + ": " + message.Content.Text})
			} else {
				finish(room.MCPServerError, 0)
				return "", errors.New("MCP prompt contains unsupported non-text content")
			}
		}
	}
	wrapped, err := WrapResult(fmt.Sprintf("%s/%s", server, promptName), output, secrets)
	if err != nil {
		finish(room.MCPServerError, 0)
		return "", err
	}
	outcome := room.MCPOk
	if output.IsError {
		outcome = room.MCPServerError
	}
	finish(outcome, len(wrapped))
	return wrapped, nil
}
