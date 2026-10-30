package roomsvc

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	harnessconfig "dyna-agent/internal/harness/config"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/slashcmd"
)

// Commands lists the available commands in a room.
func (s *Service) Commands(ctx context.Context, roomRef string) ([]slashcmd.Command, error) {
	if _, err := s.rooms().Resolve(roomRef); err != nil {
		return nil, err
	}
	catalog, err := harnessconfig.NewConfigManager().Read()
	if err != nil {
		return nil, err
	}
	commands := slashcmd.Skills(catalog.Skills)
	if s.Prompts != nil {
		prompts, err := s.Prompts.Prompts(ctx, roomRef)
		if err != nil {
			return nil, err
		}
		commands = append(commands, prompts...)
	}
	return commands, nil
}

// RunCommand sends a skill request or expanded MCP prompt as an owner post.
func (s *Service) RunCommand(ctx context.Context, roomRef, name, text, target string) (PostResult, error) {
	if strings.TrimSpace(name) == "" || strings.ContainsAny(target, " \t\r\n@") {
		return PostResult{}, errors.New("provide a command name and optional target")
	}
	rec, err := s.rooms().Resolve(roomRef)
	if err != nil {
		return PostResult{}, err
	}
	if target != "" {
		text = "@" + target + " " + text
	}
	d, err := s.parse(rec, text)
	if err != nil {
		return PostResult{}, err
	}
	var skill *room.SkillRef
	var prompt *room.PromptRef
	message := strings.TrimSpace(d.Text)
	if server, promptName, ok := strings.Cut(name, ":"); ok {
		if server == "" || promptName == "" || s.Prompts == nil {
			return PostResult{}, fmt.Errorf("no enabled command is named %q", name)
		}
		message, err = s.Prompts.RunPrompt(ctx, rec.ID, name, message)
		if err != nil {
			return PostResult{}, err
		}
		prompt = &room.PromptRef{Server: server, Prompt: promptName}
	} else {
		catalog, err := harnessconfig.NewConfigManager().Read()
		if err != nil {
			return PostResult{}, err
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
			return PostResult{}, fmt.Errorf("no enabled command is named %q", name)
		}
		if message == "" {
			return PostResult{}, errors.New("type a message for the skill")
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
	return out, err
}
