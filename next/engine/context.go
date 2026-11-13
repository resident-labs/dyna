package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/resident-labs/dyna/engine/contextview"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/discord/policy"
)

// requestChars keeps a question's line short enough for several in the section's budget.
const requestChars = 80

// holdsChars is the budget of dyna_holds, which contextview.HoldsLine also keeps to, so its first line always fits.
const holdsChars = 1000

// roomChars is the budget of dyna_room.
const roomChars = 400

// workflowsChars is the budget of dyna_workflows.
const workflowsChars = 300

// rootApprovalNames name the root-approves kinds as Settings > Approvals does.
var rootApprovalNames = map[contract.RootApproval]string{
	contract.RootApprovesAddons: "global add-ons",
	contract.RootApprovesHosts:  "room host rules",
	contract.RootApprovesPools:  "node pools",
}

func (s *Service) ContextSnapshot(ctx context.Context, caller contract.Principal, req contract.ContextSnapshotRequest) (contract.ContextSnapshotResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalSession {
		return contract.ContextSnapshotResponse{}, forbidden()
	}
	switch req.Section {
	case "", contract.ContextGoal, contract.ContextChildren, contract.ContextContacts, contract.ContextRequests, contract.ContextHolds, contract.ContextProfiles, contract.ContextUsage, contract.ContextRoom, contract.ContextDiscord, contract.ContextWorkflows:
	default:
		return contract.ContextSnapshotResponse{}, invalid("unknown context section")
	}
	data, err := s.store.ContextDataForSession(ctx, caller.Session)
	if err != nil {
		return contract.ContextSnapshotResponse{}, failure(err)
	}
	result := contract.ContextSnapshotResponse{Sections: []contract.ContextSection{}}
	add := func(key contract.ContextSectionKey, lines []string, maxChars, limit int) {
		if len(lines) > 0 && (req.Section == "" || req.Section == key) {
			result.Sections = append(result.Sections, contextview.Section(key, lines, maxChars, req.Section != "", limit))
		}
	}
	var lines []string
	for _, g := range data.Goals {
		lines = append(lines, fmt.Sprintf("%s: %s", g.ID, contextview.Excerpt(g.Text, 300)))
		if g.DoneWhen != "" {
			lines = append(lines, "Done when: "+contextview.Excerpt(g.DoneWhen, 200))
		}
		for _, item := range g.Items {
			indent := "- "
			if item.Parent != 0 {
				indent = "  - "
			}
			lines = append(lines, fmt.Sprintf("%s%d. %s", indent, item.Number, contextview.Excerpt(item.Text, 160)))
		}
	}
	add(contract.ContextGoal, lines, 1500, 0)
	lines = nil
	for _, c := range data.Children {
		line := fmt.Sprintf("%s: %s, %s", c.Name, c.Profile, c.State)
		if s.startStatus != nil && (c.State == contract.BoxCreating || c.State == contract.BoxStarting) {
			if start := s.startStatus(c.Box); start != nil {
				line += ", " + start.Label(s.now())
			}
		}
		if c.Item != "" {
			line += ", " + contextview.Excerpt(c.Item, 80)
		}
		if c.Waiting != nil {
			line += ", waiting: " + c.Waiting.Label()
		} else if c.Asking {
			line += ", waiting for the owner's answer"
		}
		lines = append(lines, line)
	}
	add(contract.ContextChildren, lines, 1000, 8)
	lines = nil
	if parent := data.Contacts.Parent; parent != nil {
		lines = append(lines, fmt.Sprintf("parent: %s (%s)", parent.Name, parent.Profile))
	}
	var siblings []string
	rootSiblings := len(data.Contacts.Siblings) > 0 && data.Contacts.Siblings[0].Room != ""
	siblingLimit := min(20, len(data.Contacts.Siblings))
	if rootSiblings && req.Section == contract.ContextContacts {
		siblingLimit = len(data.Contacts.Siblings)
	}
	for _, sibling := range data.Contacts.Siblings[:siblingLimit] {
		name := sibling.Name
		if sibling.Room != "" {
			name = string(sibling.Room) + ":" + name
		}
		siblings = append(siblings, fmt.Sprintf("%s (%s)", name, sibling.Profile))
	}
	if more := len(data.Contacts.Siblings) - len(siblings); more > 0 {
		discover := "session list"
		if rootSiblings {
			discover = "context show dyna_contacts"
		}
		siblings = append(siblings, fmt.Sprintf("and %d more (%s)", more, discover))
	}
	if len(siblings) > 0 {
		lines = append(lines, "siblings: "+strings.Join(siblings, ", "))
	}
	// The budget fits a parent and 20 siblings even at the name and profile length limits.
	add(contract.ContextContacts, lines, 3000, 0)
	lines = nil
	for _, r := range data.Requests {
		line := fmt.Sprintf("%s: %s, waiting", r.ID, r.Kind)
		if r.Text != "" {
			line += ": " + contextview.Excerpt(r.Text, requestChars)
		}
		lines = append(lines, line)
	}
	add(contract.ContextRequests, lines, 600, 0)
	lines = nil
	budget := holdsChars / 2
	if req.Section != "" {
		budget = 0
	}
	if line := contextview.HoldsLine(data.Held, budget); line != "" {
		lines = append(lines, line)
	}
	for _, p := range data.PRs {
		lines = append(lines, fmt.Sprintf("PR %s#%d: %s", p.Repo, p.Number, contextview.PRLine(p)))
	}
	add(contract.ContextHolds, lines, holdsChars, 0)
	lines = nil
	if s.maxDepth > 0 && data.Depth >= s.maxDepth {
		lines = append(lines, "none: this session is at the room's depth limit and starts no child; do the work yourself, or ask your parent to start one")
	} else {
		for _, p := range s.profiles.List() {
			line := fmt.Sprintf("%s: taste %d, diagnostics %d, intelligence %d, cost %d", p.Name, p.Taste, p.Diagnostics, p.Intelligence, p.Cost)
			if p.MaxConcurrent > 0 {
				line += fmt.Sprintf("; max concurrent %d", p.MaxConcurrent)
			}
			lines = append(lines, line)
		}
	}
	add(contract.ContextProfiles, lines, 1200, 0)
	if s.usage != nil {
		add(contract.ContextUsage, contextview.UsageLines(data.Accounts, s.usage.Latest(), s.now()), contextview.UsageChars, 0)
	}
	// One line per room setting that is not at its default and changes what root may do; a new setting adds its line here.
	lines = nil
	var decide []string
	for _, kind := range contract.AllRootApprovals {
		if data.Depth == 1 && !slices.Contains(data.RootApproves, kind) {
			decide = append(decide, rootApprovalNames[kind])
		}
	}
	if len(decide) > 0 {
		lines = append(lines, "Approvals the owner keeps: "+strings.Join(decide, ", "))
	}
	session, err := s.store.Session(ctx, caller.Session)
	if err != nil {
		return contract.ContextSnapshotResponse{}, failure(err)
	}
	account, err := s.store.GitHubAccount(ctx, session.Room)
	if err != nil {
		return contract.ContextSnapshotResponse{}, failure(err)
	}
	if account.Login != "" {
		lines = append(lines, "GitHub account: "+account.Login)
	}
	// What the room allows every session, when it is on: the GitHub writes of its githubWrites setting (spec 7.9).
	granted, err := s.GitHubWritesOf(ctx, session.Room)
	if err != nil {
		return contract.ContextSnapshotResponse{}, failure(err)
	}
	if granted.On() {
		groups := make([]string, len(granted.Groups))
		for i, group := range granted.Groups {
			groups[i] = string(group)
		}
		repos := make([]string, len(granted.Repos))
		for i, repo := range granted.Repos {
			repos[i] = string(repo)
		}
		lines = append(lines, "GitHub writes: "+strings.Join(groups, ", ")+" on "+strings.Join(repos, ", "))
	}
	repos, err := s.store.RoomRepos(ctx, session.Room)
	if err != nil {
		return contract.ContextSnapshotResponse{}, failure(err)
	}
	var prEvents []string
	for _, repo := range repos {
		if repo.PREvents {
			prEvents = append(prEvents, string(repo.Repo))
		}
	}
	if len(prEvents) > 0 {
		lines = append(lines, "Room-wide PR events: "+strings.Join(prEvents, ", "))
	}
	ciRepos, err := s.store.CIRepos(ctx)
	if err != nil {
		return contract.ContextSnapshotResponse{}, failure(err)
	}
	var runners []string
	for _, repo := range ciRepos {
		if repo.Room == session.Room {
			runners = append(runners, string(repo.Repo))
		}
	}
	if len(runners) > 0 {
		const prefix, suffix = "Dyna CI runners: ", " (dyna-ci skill)"
		names := strings.Join(runners, ", ")
		if req.Section == "" {
			budget := roomChars - len(prefix) - len(suffix)
			if len(lines) > 0 {
				budget -= len(fmt.Sprintf("\n%d more: context show", len(lines)))
			}
			if utf8.RuneCountInString(names) > budget {
				kept := 0
				for kept < len(runners) && utf8.RuneCountInString(strings.Join(runners[:kept+1], ", ")+", …") <= budget {
					kept++
				}
				names = strings.Join(runners[:kept], ", ")
				if kept > 0 {
					names += ", "
				}
				names += "…"
			}
		}
		// Put the CI line first so even a long list of settings leaves the skill discoverable.
		lines = append([]string{prefix + names + suffix}, lines...)
	}
	add(contract.ContextRoom, lines, roomChars, 0)
	routes, err := s.store.DiscordRoutes(ctx, session.Room)
	if err != nil {
		return contract.ContextSnapshotResponse{}, failure(err)
	}
	bot, err := s.store.DiscordBot(ctx)
	if err != nil {
		return contract.ContextSnapshotResponse{}, failure(err)
	}
	tree, err := s.store.RoomTree(ctx, session.Room)
	if err != nil {
		return contract.ContextSnapshotResponse{}, failure(err)
	}
	lines = nil
	allowed := s.discordOnPath(caller.Session, routes, tree)
	if bot.ID != "" && allowed {
		for _, r := range routes {
			answering, err := s.store.Session(ctx, r.Session)
			if err != nil {
				return contract.ContextSnapshotResponse{}, failure(err)
			}
			server := r.Guild
			if g, ok := s.discordDirectory.Server(r.Guild); ok {
				server = fmt.Sprintf("%s (ID %s)", g.Name, r.Guild)
			}
			place := "server " + server
			if r.Channel != "" {
				channel := r.Channel
				if c, ok := s.discordDirectory.Channel(r.Channel); ok {
					channel = fmt.Sprintf("%s (ID %s)", c.Name, r.Channel)
				}
				place = "#" + channel + " of " + server
			}
			line := fmt.Sprintf("Discord: bot %s; %s, answered by %s", bot.Name, place, answering.Name)
			if r.PolicyResolved != "" {
				var p policy.Policy
				if json.Unmarshal([]byte(r.PolicyResolved), &p) == nil && p.On() {
					line += "\nPolicy: on; the answering session is capped (discord policy)"
				}
			}
			if r.Channel == "" {
				// The live list, so a session knows a channel another session made. Categories, voice and threads are left out.
				var channels []string
				for _, c := range s.discordDirectory.Channels() {
					if c.Guild == r.Guild && (c.Type == 0 || c.Type == 5 || c.Type == 15) {
						channels = append(channels, fmt.Sprintf("#%s (%s)", c.Name, c.ID))
					}
				}
				if len(channels) > 50 {
					channels = append(channels[:50], "more: GET /guilds/"+r.Guild+"/channels")
				}
				line += "\nChannels: " + strings.Join(channels, ", ")
			}
			if caller.Session == r.Session && r.Instructions != "" {
				line += "\nYour standing instructions, from the owner or root:\n" + r.Instructions
			}
			lines = append(lines, line)
		}
	}
	add(contract.ContextDiscord, lines, 20000, 0)
	// What the room's workflow settings change from the defaults, for every session; limits and defaults are named by the refusal that
	// meets them.
	effective, err := s.store.WorkflowEffective(ctx, session.Room)
	if err != nil {
		return contract.ContextSnapshotResponse{}, failure(err)
	}
	lines = nil
	switch effective.Start {
	case contract.WorkflowStartLeads:
		lines = append(lines, "Workflows: started by leads (root and sessions that have started a child)")
	case contract.WorkflowStartRoot:
		lines = append(lines, "Workflows: started by root")
	case contract.WorkflowStartOff:
		lines = append(lines, "Workflows: no session starts one; the owner does")
	}
	var off []string
	for _, feature := range contract.AllWorkflowFeatures {
		if !effective.Allows(feature) {
			name := string(feature)
			if feature != contract.WorkflowFeatureResume {
				name += "()"
			}
			off = append(off, name)
		}
	}
	if len(off) > 0 {
		lines = append(lines, "Off: "+strings.Join(off, ", "))
	}
	add(contract.ContextWorkflows, lines, workflowsChars, 0)
	return result, nil
}
