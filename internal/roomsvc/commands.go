package roomsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	harnessconfig "dyna-agent/internal/harness/config"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/slashcmd"
	"dyna-agent/internal/workflowcatalog"
)

// Commands lists the available commands in a room.
func (s *Service) Commands(ctx context.Context, roomRef string) ([]slashcmd.Command, error) {
	rec, err := s.rooms().Resolve(roomRef)
	if err != nil {
		return nil, err
	}
	catalog, err := harnessconfig.NewConfigManager().Read()
	if err != nil {
		return nil, err
	}
	var workflows []workflowcatalog.Info
	if rec.Repo != "" {
		workflows, err = s.ListWorkflows(roomRef)
		if err != nil {
			return nil, err
		}
	}
	commands := make([]slashcmd.Command, 0, len(workflows))
	seen := make(map[string]bool)
	for _, workflow := range workflows {
		if !seen[workflow.Name] {
			commands = append(commands, slashcmd.Command{Name: workflow.Name, Source: slashcmd.Workflow, Description: workflow.Description, Args: []slashcmd.Arg{}})
			seen[workflow.Name] = true
		}
	}
	for _, skill := range slashcmd.Skills(catalog.Skills) {
		if !seen[skill.Name] {
			commands = append(commands, skill)
		}
	}
	if s.Prompts != nil {
		prompts, err := s.Prompts.Prompts(ctx, roomRef)
		if err != nil {
			return nil, err
		}
		commands = append(commands, prompts...)
	}
	return commands, nil
}

type CommandResult struct {
	PostResult
	Run *StartedRun `json:"run,omitempty"`
}

// RunCommand starts a workflow or sends a skill or prompt as an owner post.
func (s *Service) RunCommand(ctx context.Context, roomRef, name, text, target string) (CommandResult, error) {
	if strings.TrimSpace(name) == "" || strings.ContainsAny(target, " \t\r\n@") {
		return CommandResult{}, errors.New("provide a command name and optional target")
	}
	rec, err := s.rooms().Resolve(roomRef)
	if err != nil {
		return CommandResult{}, err
	}
	var workflows []workflowcatalog.Info
	if rec.Repo != "" {
		workflows, err = s.ListWorkflows(roomRef)
		if err != nil {
			return CommandResult{}, err
		}
	}
	for _, workflow := range workflows {
		if workflow.Name == name {
			input, err := json.Marshal(strings.TrimSpace(text))
			if err != nil {
				return CommandResult{}, err
			}
			run, err := s.StartNamedRun(ctx, StartNamedRunRequest{Room: rec.ID, Name: name, Input: input, ResultTo: target})
			if err != nil {
				return CommandResult{}, err
			}
			return CommandResult{Run: &run}, nil
		}
	}
	parseText := text
	if target != "" {
		parseText = "@" + target
	}
	d, err := s.parse(rec, parseText)
	if err != nil {
		return CommandResult{}, err
	}
	var skill *room.SkillRef
	var prompt *room.PromptRef
	message := strings.TrimSpace(d.Text)
	if target != "" {
		message = strings.TrimSpace(text)
	}
	if server, promptName, ok := strings.Cut(name, ":"); ok {
		if server == "" || promptName == "" || s.Prompts == nil {
			return CommandResult{}, fmt.Errorf("no enabled command is named %q", name)
		}
		message, err = s.Prompts.RunPrompt(ctx, rec.ID, name, message)
		if err != nil {
			return CommandResult{}, err
		}
		prompt = &room.PromptRef{Server: server, Prompt: promptName}
	} else {
		catalog, err := harnessconfig.NewConfigManager().Read()
		if err != nil {
			return CommandResult{}, err
		}
		for _, command := range slashcmd.Skills(catalog.Skills) {
			if command.Name == name {
				for _, entry := range catalog.Skills {
					if entry.Enabled && entry.Name == name {
						skill = &room.SkillRef{Name: name, Path: filepath.Join(entry.Source, "SKILL.md")}
						break
					}
				}
				break
			}
		}
		if skill == nil {
			return CommandResult{}, fmt.Errorf("no enabled command is named %q", name)
		}
		if message == "" {
			return CommandResult{}, errors.New("type a message for the skill")
		}
	}
	res, err := s.Notify.Post(ctx, notify.PostInput{
		Sender:   notify.Sender{Kind: notify.SenderOwner, Room: rec.Name, Human: true},
		Mentions: d.Mentions, Text: message, Skill: skill, Prompt: prompt,
	})
	out := PostResult{Message: res.Message, Deliveries: res.Deliveries, Failed: res.Failed(), Sticky: d.Sticky()}
	if res.Message.ID != "" {
		_ = s.rooms().MarkOwnerRead(res.Message.RoomID, res.Message.ID)
	}
	if errors.Is(err, notify.ErrDelivery) && res.Message.ID != "" {
		err = nil
	}
	return CommandResult{PostResult: out}, err
}
