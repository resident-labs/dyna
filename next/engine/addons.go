package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/resident-labs/dyna/box/skills"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/addons"
	"github.com/resident-labs/dyna/engine/approve"
)

var mcpName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Addons is the store of skills, packages and MCP servers, which hosts mirror beside instructions.
func (s *Service) Addons() *addons.Files { return s.addons }

// addonTarget is where a change lands. ask is the session's room when the change must wait for the owner.
type addonTarget struct {
	room contract.RoomID
	by   string
	ask  contract.RoomID
}

// targetOf applies the one rule: a session changes its own room, and its global changes wait for the owner.
func (s *Service) targetOf(ctx context.Context, caller contract.Principal, room contract.RoomID, global bool) (addonTarget, *contract.Error) {
	switch caller.Kind {
	case contract.PrincipalOwner:
		if global == (room != "") {
			return addonTarget{}, invalid("give a room, or global for every room")
		}
		if !global {
			if err := s.store.RoomExists(ctx, room); err != nil {
				return addonTarget{}, failure(err)
			}
		}
		return addonTarget{room: room, by: "owner"}, nil
	case contract.PrincipalSession:
		session, err := s.store.Session(ctx, caller.Session)
		if err != nil {
			return addonTarget{}, failure(err)
		}
		if room != "" && room != session.Room {
			return addonTarget{}, forbidden()
		}
		if global {
			return addonTarget{by: session.Name, ask: session.Room}, nil
		}
		return addonTarget{room: session.Room, by: session.Name}, nil
	}
	return addonTarget{}, forbidden()
}

// addonChange makes a change now, or asks the owner.
func (s *Service) addonChange(ctx context.Context, caller contract.Principal, target addonTarget, change contract.AddonGlobalArgs, archive []byte) (contract.AddonChangeResponse, *contract.Error) {
	if target.ask != "" {
		op, err := s.approvals.Submit(ctx, caller, contract.Operation{Room: target.ask, Kind: change.OpKind(), Args: change})
		if err != nil {
			return contract.AddonChangeResponse{}, failure(err)
		}
		return contract.AddonChangeResponse{Operation: op.ID}, nil
	}
	item, err := s.applyAddon(ctx, target, change, archive)
	if err != nil {
		if errors.Is(err, addons.ErrMissing) {
			return contract.AddonChangeResponse{}, &contract.Error{Code: contract.ErrNotFound, Message: fmt.Sprintf("no %s named %s", change.Kind, change.Name)}
		}
		return contract.AddonChangeResponse{}, failure(err)
	}
	return contract.AddonChangeResponse{Item: item}, nil
}

func (s *Service) applyAddon(ctx context.Context, target addonTarget, change contract.AddonGlobalArgs, archive []byte) (*contract.Addon, error) {
	at := s.now()
	item := contract.Addon{Kind: change.Kind, Name: change.Name, Room: target.room, By: target.by, At: &at, Description: change.Description, Source: change.Source, URL: change.URL, Command: change.Command, Args: change.Args}
	apply := func() error {
		switch {
		case change.Remove:
			return s.addons.Remove(target.room, change.Kind, change.Name)
		case change.Kind == contract.AddonSkill:
			return s.addons.SaveSkill(target.room, item, archive)
		}
		return s.addons.Put(target.room, item)
	}
	var err error
	if target.room == "" {
		err = apply()
	} else {
		err = s.roomWrite(ctx, target.room, apply)
	}
	if err != nil {
		return nil, err
	}
	s.publishAddons(ctx)
	if change.Remove {
		return nil, nil
	}
	return &item, nil
}

func (s *Service) publishAddons(ctx context.Context) {
	if s.publishInstructions != nil {
		s.publishInstructions(context.WithoutCancel(ctx))
	}
}

// PackageAdd pins the source before anything else, so the owner approves what every box will get.
func (s *Service) PackageAdd(ctx context.Context, caller contract.Principal, req contract.PackageAddRequest) (contract.AddonChangeResponse, *contract.Error) {
	target, problem := s.targetOf(ctx, caller, req.Room, req.Global)
	if problem != nil {
		return contract.AddonChangeResponse{}, problem
	}
	name, source, err := s.addonSources.Resolve(ctx, req.Source)
	if err != nil {
		return contract.AddonChangeResponse{}, invalid(err.Error())
	}
	return s.addonChange(ctx, caller, target, contract.AddonGlobalArgs{Kind: contract.AddonPackage, Name: name, Source: source}, nil)
}

func checkMCP(name, link, command string, args []string) *contract.Error {
	switch {
	case !mcpName.MatchString(name):
		return invalid("an MCP server name is 1 to 64 letters, digits, underscores or hyphens")
	case name == "dyna" || name == "shell":
		return invalid(name + " is the box's own MCP server")
	case (link == "") == (command == ""):
		return invalid("give a url or a command")
	case link != "" && len(args) > 0:
		return invalid("args go with a command, not a url")
	}
	if link != "" {
		if parsed, err := url.Parse(link); err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
			return invalid("an MCP server url is http or https")
		}
	}
	return nil
}

func (s *Service) MCPAdd(ctx context.Context, caller contract.Principal, req contract.MCPAddRequest) (contract.AddonChangeResponse, *contract.Error) {
	target, problem := s.targetOf(ctx, caller, req.Room, req.Global)
	if problem != nil {
		return contract.AddonChangeResponse{}, problem
	}
	if problem := checkMCP(req.Name, req.URL, req.Command, req.Args); problem != nil {
		return contract.AddonChangeResponse{}, problem
	}
	return s.addonChange(ctx, caller, target, contract.AddonGlobalArgs{Kind: contract.AddonMCP, Name: req.Name, URL: req.URL, Command: req.Command, Args: req.Args}, nil)
}

// SkillSave reads the skill now. A global save from a session keeps the archive among its room's files until the owner decides.
func (s *Service) SkillSave(ctx context.Context, caller contract.Principal, req contract.SkillSaveRequest) (contract.AddonChangeResponse, *contract.Error) {
	target, problem := s.targetOf(ctx, caller, req.Room, req.Global)
	if problem != nil {
		return contract.AddonChangeResponse{}, problem
	}
	if (req.URL == "") == (len(req.Archive) == 0) {
		return contract.AddonChangeResponse{}, invalid("give a folder or a link")
	}
	archive := req.Archive
	if req.URL != "" {
		var err error
		if archive, err = s.addonSources.Skill(ctx, req.URL); err != nil {
			return contract.AddonChangeResponse{}, invalid(err.Error())
		}
	}
	meta, err := skills.Inspect(archive)
	if err != nil {
		return contract.AddonChangeResponse{}, invalid(err.Error())
	}
	if skills.IsBuiltIn(meta.Name) {
		return contract.AddonChangeResponse{}, invalid(meta.Name + " is a built-in skill; pick another name")
	}
	change := contract.AddonGlobalArgs{Kind: contract.AddonSkill, Name: meta.Name, Description: meta.Description, Source: req.URL}
	if target.ask != "" {
		if change.Bundle, _, err = s.saveFile(ctx, target.ask, bytes.NewReader(archive), 25<<20); err != nil {
			return contract.AddonChangeResponse{}, failure(err)
		}
	}
	return s.addonChange(ctx, caller, target, change, archive)
}

func (s *Service) addonRemove(ctx context.Context, caller contract.Principal, kind contract.AddonKind, req contract.AddonRemoveRequest) (contract.AddonChangeResponse, *contract.Error) {
	target, problem := s.targetOf(ctx, caller, req.Room, req.Global)
	if problem != nil {
		return contract.AddonChangeResponse{}, problem
	}
	// Asking the owner about something that is not there would only cost a card.
	found, err := s.addons.Has(target.room, kind, req.Name)
	if err != nil {
		return contract.AddonChangeResponse{}, failure(err)
	}
	if !found {
		return contract.AddonChangeResponse{}, &contract.Error{Code: contract.ErrNotFound, Message: fmt.Sprintf("no %s named %s", kind, req.Name)}
	}
	return s.addonChange(ctx, caller, target, contract.AddonGlobalArgs{Remove: true, Kind: kind, Name: req.Name}, nil)
}

func (s *Service) PackageRemove(ctx context.Context, caller contract.Principal, req contract.AddonRemoveRequest) (contract.AddonChangeResponse, *contract.Error) {
	return s.addonRemove(ctx, caller, contract.AddonPackage, req)
}

func (s *Service) SkillRemove(ctx context.Context, caller contract.Principal, req contract.AddonRemoveRequest) (contract.AddonChangeResponse, *contract.Error) {
	return s.addonRemove(ctx, caller, contract.AddonSkill, req)
}

func (s *Service) MCPRemove(ctx context.Context, caller contract.Principal, req contract.AddonRemoveRequest) (contract.AddonChangeResponse, *contract.Error) {
	return s.addonRemove(ctx, caller, contract.AddonMCP, req)
}

// addonList gathers the scopes a caller can see, built-ins first, then global, then rooms. An empty kind means all three.
func (s *Service) addonList(ctx context.Context, caller contract.Principal, kind contract.AddonKind, room contract.RoomID) (contract.AddonListResponse, *contract.Error) {
	rooms := []contract.RoomID{room}
	switch {
	case caller.Kind == contract.PrincipalSession:
		session, err := s.store.Session(ctx, caller.Session)
		if err != nil {
			return contract.AddonListResponse{}, failure(err)
		}
		if room != "" && room != session.Room {
			return contract.AddonListResponse{}, forbidden()
		}
		rooms[0] = session.Room
	case room == "":
		var err error
		if rooms, err = s.store.OpenRooms(ctx); err != nil {
			return contract.AddonListResponse{}, failure(err)
		}
	default:
		if err := s.store.RoomExists(ctx, room); err != nil {
			return contract.AddonListResponse{}, failure(err)
		}
	}
	items := skills.BuiltIn()
	for _, scope := range append([]contract.RoomID{""}, rooms...) {
		held, err := s.addons.List(scope)
		if err != nil {
			return contract.AddonListResponse{}, failure(err)
		}
		items = append(items, held...)
	}
	out := contract.AddonListResponse{Items: []contract.Addon{}}
	for _, item := range items {
		if kind == "" || item.Kind == kind {
			out.Items = append(out.Items, item)
		}
	}
	return out, nil
}

func (s *Service) AddonList(ctx context.Context, caller contract.Principal, req contract.AddonListRequest) (contract.AddonListResponse, *contract.Error) {
	return s.addonList(ctx, caller, "", req.Room)
}

func (s *Service) PackageList(ctx context.Context, caller contract.Principal, req contract.AddonListRequest) (contract.AddonListResponse, *contract.Error) {
	return s.addonList(ctx, caller, contract.AddonPackage, req.Room)
}

func (s *Service) SkillList(ctx context.Context, caller contract.Principal, req contract.AddonListRequest) (contract.AddonListResponse, *contract.Error) {
	return s.addonList(ctx, caller, contract.AddonSkill, req.Room)
}

func (s *Service) MCPList(ctx context.Context, caller contract.Principal, req contract.AddonListRequest) (contract.AddonListResponse, *contract.Error) {
	return s.addonList(ctx, caller, contract.AddonMCP, req.Room)
}

// AddonSetEnabled is the owner's: the method table lets nobody else call it.
func (s *Service) AddonSetEnabled(ctx context.Context, _ contract.Principal, req contract.AddonSetEnabledRequest) (contract.AddonSetEnabledResponse, *contract.Error) {
	apply := func() error { return s.addons.SetEnabled(req.Room, req.Kind, req.Name, req.Enabled) }
	var err error
	if req.Room == "" {
		err = apply()
	} else {
		err = s.roomWrite(ctx, req.Room, apply)
	}
	if errors.Is(err, addons.ErrMissing) {
		return contract.AddonSetEnabledResponse{}, &contract.Error{Code: contract.ErrNotFound, Message: fmt.Sprintf("no %s named %s", req.Kind, req.Name)}
	}
	if err != nil {
		return contract.AddonSetEnabledResponse{}, failure(err)
	}
	s.publishAddons(ctx)
	return contract.AddonSetEnabledResponse{}, nil
}

// AddonGlobal registers a session's global change. A skill's folder stays out of the operation's record, among the room's files.
func (s *Service) AddonGlobal() approve.Entry {
	return approve.Entry{
		Scope: contract.ScopeAdmin,
		Class: func(contract.OpArgs, contract.Principal) contract.Class { return contract.ClassOwner },
		Card: func(_ context.Context, args contract.OpArgs, _ contract.Principal) contract.Card {
			input := args.(contract.AddonGlobalArgs)
			kind := map[contract.AddonKind]string{contract.AddonSkill: "a skill", contract.AddonPackage: "a Pi package", contract.AddonMCP: "an MCP server"}[input.Kind]
			if input.Remove {
				return contract.Card{Title: "asks to remove " + kind + " from every room", Body: input.Name}
			}
			detail := []string{input.Name}
			for _, part := range append([]string{input.Source, input.Description, input.URL, input.Command}, input.Args...) {
				if part != "" {
					detail = append(detail, part)
				}
			}
			return contract.Card{Title: "asks to add " + kind + " for every room", Body: strings.Join(detail, "\n")}
		},
		Check: func(ctx context.Context, args contract.OpArgs, _ contract.Principal) error {
			input := args.(contract.AddonGlobalArgs)
			if input.Remove {
				found, err := s.addons.Has("", input.Kind, input.Name)
				if err == nil && !found {
					err = fmt.Errorf("%w: no %s named %s", approve.ErrInvalid, input.Kind, input.Name)
				}
				return err
			}
			switch input.Kind {
			case contract.AddonPackage:
				// Resolving a pinned source needs no network, and it must give the same thing back.
				name, source, err := s.addonSources.Resolve(ctx, input.Source)
				if err != nil || name != input.Name || source != input.Source {
					return approve.ErrInvalid
				}
			case contract.AddonSkill:
				if !validHash(input.Bundle) {
					return approve.ErrInvalid
				}
			case contract.AddonMCP:
				if problem := checkMCP(input.Name, input.URL, input.Command, input.Args); problem != nil {
					return fmt.Errorf("%w: %s", approve.ErrInvalid, problem.Message)
				}
			default:
				return approve.ErrInvalid
			}
			return nil
		},
		Decode: func(data json.RawMessage) (contract.OpArgs, error) {
			var input contract.AddonGlobalArgs
			err := json.Unmarshal(data, &input)
			return input, err
		},
		Execute: func(ctx context.Context, _ contract.OpID, args contract.OpArgs, who contract.Principal) contract.OpResult {
			input := args.(contract.AddonGlobalArgs)
			session, err := s.store.Session(ctx, who.Session)
			if err != nil {
				return contract.OpResult{Cause: contract.CauseInternal, Error: "the session that asked is gone"}
			}
			var archive []byte
			if input.Bundle != "" {
				if archive, err = os.ReadFile(s.layout.File(session.Room, input.Bundle)); err != nil {
					return contract.OpResult{Cause: contract.CauseInternal, Error: "the skill's folder is gone: " + err.Error()}
				}
				// The archive names the skill. The operation's own name only described it.
				meta, err := skills.Inspect(archive)
				if err != nil {
					return contract.OpResult{Cause: contract.CauseInternal, Error: err.Error()}
				}
				if skills.IsBuiltIn(meta.Name) {
					return contract.OpResult{Cause: contract.CauseInternal, Error: meta.Name + " is a built-in skill"}
				}
				input.Name, input.Description = meta.Name, meta.Description
			}
			if _, err := s.applyAddon(ctx, addonTarget{by: session.Name}, input, archive); err != nil {
				return contract.OpResult{Cause: contract.CauseInternal, Error: err.Error()}
			}
			return contract.OpResult{Output: input.Name + " reaches each session at its next box start."}
		},
	}
}

// BoxNotice records what a box could not set up. It waits for the session's next turn, because a failed add-on is worth
// knowing but not worth a model call by itself, and a session that keeps failing must not keep waking anybody.
func (s *Service) BoxNotice(ctx context.Context, caller contract.Principal, req contract.BoxNoticeRequest) (contract.BoxNoticeResponse, *contract.Error) {
	session, err := s.store.Session(ctx, caller.Session)
	if err != nil {
		return contract.BoxNoticeResponse{}, failure(err)
	}
	text := req.Text
	if len(text) > 4096 {
		text = strings.ToValidUTF8(text[:4096], "")
	}
	event, err := s.engineEvent(session.Room, contract.SessionNotice{Session: session.ID, Text: text})
	if err != nil {
		return contract.BoxNoticeResponse{}, failure(err)
	}
	if err := s.store.SendSessionNotice(ctx, event, session.ID); err != nil {
		return contract.BoxNoticeResponse{}, failure(err)
	}
	return contract.BoxNoticeResponse{}, nil
}
