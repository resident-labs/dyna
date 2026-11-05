package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

// workflowRefusal is a call that a workflow setting refused. The cause names the setting, and the message says what the setting
// is, so a reader who sees only the message learns where to look.
func workflowRefusal(setting contract.WorkflowSetting, message string) *contract.Error {
	return &contract.Error{Code: contract.ErrForbidden, Message: fmt.Sprintf("%s (workflow setting %s)", message, setting), Workflow: &contract.WorkflowRefused{Setting: setting}}
}

// workflowStarters says whether the caller may start a workflow in a room under its settings. The owner always may. A session
// that hosts a run does not: a script nests another with workflow().
func (s *Service) workflowStarters(ctx context.Context, caller contract.Principal, roomID contract.RoomID, effective contract.WorkflowEffective) *contract.Error {
	if caller.Kind != contract.PrincipalSession {
		return nil
	}
	if _, err := s.store.SessionWorkflow(ctx, caller.Session); err == nil {
		return &contract.Error{Code: contract.ErrForbidden, Message: "a workflow's crew starts no workflow of its own; a script runs another with workflow()"}
	} else if !errors.Is(err, store.ErrNotFound) {
		return failure(err)
	}
	tree, err := s.store.RoomTree(ctx, roomID)
	if err != nil {
		return failure(err)
	}
	root := caller.Session == tree.Root()
	switch effective.Start {
	case contract.WorkflowStartAny:
		return nil
	case contract.WorkflowStartRoot:
		if root {
			return nil
		}
		return workflowRefusal(contract.WorkflowSettingStart, "workflows are started by the room's root only")
	case contract.WorkflowStartLeads:
		if root {
			return nil
		}
		lead, err := s.store.SessionHasChildren(ctx, caller.Session)
		if err != nil {
			return failure(err)
		}
		if lead {
			return nil
		}
		return workflowRefusal(contract.WorkflowSettingStart, "workflows are started by the room's root and by sessions that have started a child")
	}
	return workflowRefusal(contract.WorkflowSettingStart, "no session starts a workflow in this room; the owner does")
}

// workflowProfiles refuses a script that names a profile the room's settings leave out.
func workflowProfiles(effective contract.WorkflowEffective, named []contract.ProfileName) *contract.Error {
	for _, name := range named {
		if len(effective.Profiles) > 0 && !slices.Contains(effective.Profiles, name) {
			return workflowRefusal(contract.WorkflowSettingProfiles, fmt.Sprintf("profile %s is not among the profiles this room's workflows may use: %v", name, effective.Profiles))
		}
	}
	return nil
}

// WorkflowSettings reads the settings: the built-in defaults, the global values and each open room's, or one room's.
func (s *Service) WorkflowSettings(ctx context.Context, caller contract.Principal, req contract.WorkflowSettingsRequest) (contract.WorkflowSettingsResponse, *contract.Error) {
	var response contract.WorkflowSettingsResponse
	if caller.Kind != contract.PrincipalOwner {
		return response, forbidden()
	}
	all, err := s.store.WorkflowSettings(ctx)
	if err != nil {
		return response, failure(err)
	}
	listed, _, err := s.store.RoomList(ctx)
	if err != nil {
		return response, failure(err)
	}
	global := all[store.GlobalWorkflowScope]
	inherited := global.Over(contract.WorkflowDefaults)
	response = contract.WorkflowSettingsResponse{Settings: contract.AllWorkflowSettings, Defaults: contract.WorkflowDefaults, Global: contract.WorkflowScope{Set: global, Effective: inherited}, Rooms: []contract.WorkflowScope{}}
	for _, room := range listed.Rooms {
		if room.Deleting || req.Room != "" && room.ID != req.Room {
			continue
		}
		response.Rooms = append(response.Rooms, contract.WorkflowScope{Room: room.ID, Name: room.Name, Set: all[room.ID], Effective: all[room.ID].Over(inherited)})
	}
	if req.Room != "" && len(response.Rooms) == 0 {
		return response, &contract.Error{Code: contract.ErrNotFound, Message: "no room has that ID"}
	}
	return response, nil
}

// WorkflowSet changes the global workflow values or one room's overrides. Only the owner reaches it. Every value is checked
// here, once, so the rest of the engine reads values it can trust.
func (s *Service) WorkflowSet(ctx context.Context, caller contract.Principal, req contract.WorkflowSetRequest) (contract.WorkflowSetResponse, *contract.Error) {
	var response contract.WorkflowSetResponse
	if caller.Kind != contract.PrincipalOwner {
		return response, forbidden()
	}
	set := req.Set
	named := set.Named()
	if len(named)+len(req.Clear) == 0 {
		return response, invalid("set or clear at least one setting")
	}
	known := make([]contract.WorkflowSetting, 0, len(contract.AllWorkflowSettings))
	for _, info := range contract.AllWorkflowSettings {
		known = append(known, info.Setting)
	}
	for _, setting := range req.Clear {
		if !slices.Contains(known, setting) {
			return response, invalid(fmt.Sprintf("no workflow setting is named %q; the settings are %v", setting, known))
		}
		if slices.Contains(named, setting) {
			return response, invalid(fmt.Sprintf("%s is both set and cleared", setting))
		}
	}
	if set.Start != "" && !slices.Contains(contract.AllWorkflowStarters, set.Start) {
		return response, invalid(fmt.Sprintf("start is %q; it is one of %v", set.Start, contract.AllWorkflowStarters))
	}
	for name, value := range map[contract.WorkflowSetting]*int{contract.WorkflowSettingMaxRuns: set.MaxRuns, contract.WorkflowSettingMaxWorkers: set.MaxWorkers} {
		if value != nil && *value < 0 {
			return response, invalid(string(name) + " is 0 for no limit, or a count")
		}
	}
	if set.Timeout != nil && *set.Timeout < 1 {
		return response, invalid("timeout is a number of seconds, at least 1")
	}
	if _, ok := box.Size(set.Size); set.Size != "" && !ok {
		return response, invalid(fmt.Sprintf("size %q is not a box size", set.Size))
	}
	for _, name := range set.Profiles {
		if _, _, err := s.profiles.Model(name); err != nil {
			return response, invalid("unknown profile " + string(name))
		}
	}
	if req.Room != store.GlobalWorkflowScope {
		if err := s.store.RoomOpen(ctx, req.Room); err != nil {
			return response, failure(err)
		}
	}
	err := s.store.ChangeWorkflowSettings(ctx, req.Room, func(current contract.WorkflowSettings) contract.WorkflowSettings {
		return current.Changed(set, req.Clear)
	})
	if err != nil {
		return response, failure(err)
	}
	return response, nil
}
