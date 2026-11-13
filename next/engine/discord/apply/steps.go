package apply

import (
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/resident-labs/dyna/engine/discord/folder"
)

// The order Discord needs. Community's channels exist before Community is
// enabled, and forum and announcement channels come after it.
type phase int

const (
	rolePhase phase = iota
	channelPhase
	settingsPhase
	communityChannelPhase
	overwritePhase
	orderPhase
	roleOrderPhase
	autoModPhase
	messagePhase
	prunePhase
	policyPhase
	phaseCount
)

var (
	channelTypes = map[string]int{"text": 0, "voice": 2, "announcement": 5, "forum": 15}
	verification = []string{"none", "low", "medium", "high", "very_high"}
	notification = []string{"all", "mentions"}
)

func (r *run) add(when phase, text string, do func() error) {
	r.phases[when] = append(r.phases[when], step{text, do})
}

// change is the plan's field changes of one object, merged into one change.
type change struct {
	folder.Change
	Fields map[string]any // field label to its new value
}

func (c change) String() string {
	switch c.Operation {
	case folder.Create:
		return fmt.Sprintf("create %s %s", c.Kind, c.Name)
	case folder.EditMessage:
		return fmt.Sprintf("edit message %s in place", c.Name)
	}
	fields := make([]string, 0, len(c.Fields))
	for field := range c.Fields {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	return fmt.Sprintf("change %s %s: %s", c.Kind, c.Name, strings.Join(fields, ", "))
}

func (r *run) addChanges() {
	var merged []change
	for _, planned := range r.plan.Changes {
		last := len(merged) - 1
		if planned.Operation == folder.ChangeFields && last >= 0 && merged[last].Operation == folder.ChangeFields &&
			merged[last].Kind == planned.Kind && merged[last].Name == planned.Name && merged[last].ID == planned.ID {
			merged[last].Fields[planned.Field] = planned.After
			continue
		}
		merged = append(merged, change{planned, map[string]any{planned.Field: planned.After}})
	}
	for _, c := range merged {
		switch c.Kind {
		case folder.RoleKind:
			r.addRole(c)
		case folder.CategoryKind, folder.ChannelKind:
			r.addChannel(c)
		case folder.ServerKind:
			r.addServer(c)
		case folder.AutoModKind:
			r.addAutoMod(c)
		case folder.MessageKind:
			r.addMessage(c)
		case folder.PolicyKind:
			r.add(policyPhase, c.String(), func() error { return r.target.SetPolicy(r.ctx, r.desired.Policy) })
		}
	}
}

func (r *run) guildRoute(suffix string) string { return "/guilds/" + r.target.Guild + suffix }

func (r *run) addRole(c change) {
	role := r.roles[c.Name]
	if c.Operation == folder.Create {
		r.add(rolePhase, c.String(), func() error {
			_, err := r.create(Owned{Kind: string(c.Kind), Path: c.Name}, r.guildRoute("/roles"), roleBody(role, 0, nil))
			return err
		})
		return
	}
	// Permission bits the folder cannot name stay as they are on the server.
	var otherBits uint64
	for _, live := range r.live.Server.Roles {
		if live.ID == c.ID {
			otherBits = live.OtherPermissions
		}
	}
	body := roleBody(role, otherBits, c.Fields)
	if len(body) > 0 {
		r.add(rolePhase, c.String(), func() error {
			_, err := r.call("PATCH", r.guildRoute("/roles/"+c.ID), body)
			return err
		})
	}
}

// roleBody holds the fields of a role, or only those in only.
func roleBody(role folder.Role, otherBits uint64, only map[string]any) map[string]any {
	all := map[string]any{
		"name":        role.Name,
		"permissions": strconv.FormatUint(permissionBits(role.Permissions)|otherBits, 10),
		"color":       colorValue(role.Color),
		"hoist":       role.Hoist,
		"mentionable": role.Mentionable,
	}
	if only == nil {
		return all
	}
	body := map[string]any{}
	for field := range only {
		if value, wanted := all[field]; wanted {
			body[field] = value
		}
	}
	return body
}

func permissionBits(names []string) uint64 {
	var bits uint64
	for _, name := range names {
		bits |= folder.PermissionBits[name]
	}
	return bits
}

func colorValue(color string) int64 {
	value, _ := strconv.ParseInt(strings.TrimPrefix(color, "#"), 16, 32) // Load checked the format
	return value
}

// overwrites builds a channel's permission_overwrites from role names, "everyone"
// and "member:ID".
func (r *run) overwrites(wanted folder.Overwrites) ([]map[string]any, error) {
	type bits struct{ allow, deny uint64 }
	targets := map[string]*bits{}
	for name, names := range wanted.Allow {
		targets[name] = &bits{allow: permissionBits(names)}
	}
	for name, names := range wanted.Deny {
		if targets[name] == nil {
			targets[name] = &bits{}
		}
		targets[name].deny = permissionBits(names)
	}
	result := []map[string]any{}
	for _, name := range slices.Sorted(mapKeys(targets)) {
		id, kind := r.target.Guild, 0
		if member, isMember := strings.CutPrefix(name, "member:"); isMember {
			id, kind = member, 1
		} else if name != "everyone" {
			if id = r.ids[folder.Subject{Kind: folder.RoleKind, Name: name}]; id == "" {
				return nil, fmt.Errorf("role %s has no Discord ID", name)
			}
		}
		result = append(result, map[string]any{"id": id, "type": kind, "allow": strconv.FormatUint(targets[name].allow, 10), "deny": strconv.FormatUint(targets[name].deny, 10)})
	}
	return result, nil
}

func mapKeys[V any](m map[string]V) func(yield func(string) bool) {
	return func(yield func(string) bool) {
		for key := range m {
			if !yield(key) {
				return
			}
		}
	}
}

func slowmodeSeconds(slowmode string) int {
	duration, _ := time.ParseDuration(slowmode) // Load checked the format
	return int(duration / time.Second)
}

func (r *run) addChannel(c change) {
	entry := r.entries[c.Name]
	if c.Operation == folder.Create {
		when := channelPhase
		if entry.Channel != nil && (entry.Channel.Type == "forum" || entry.Channel.Type == "announcement") {
			when = communityChannelPhase
		}
		r.add(when, c.String(), func() error {
			body, err := r.createBody(entry)
			if err != nil {
				return err
			}
			_, err = r.create(Owned{Kind: string(c.Kind), Path: c.Name}, r.guildRoute("/channels"), body)
			return err
		})
		if entry.Category != nil {
			r.addChannelOrder(entry)
		}
		return
	}
	body := map[string]any{}
	for field, after := range c.Fields {
		switch field {
		case "Name":
			body["name"] = entry.Name
		case "Parent":
			parent, _ := after.(string)
			body["parent_id"] = nil
			if parent != "" {
				body["parent_id"] = r.categoryID(parent)
			}
		case "topic":
			body["topic"] = entry.Channel.Topic
		case "slowmode":
			body["rate_limit_per_user"] = slowmodeSeconds(entry.Channel.Slowmode)
		case "type", "tags":
			r.byHand = append(r.byHand, fmt.Sprintf("%s %s: change %s in Discord; apply does not change it on an existing channel", c.Kind, c.Name, field))
		case "channels":
			r.addChannelOrder(entry)
		}
	}
	if len(body) > 0 {
		r.add(channelPhase, c.String(), func() error {
			_, err := r.call("PATCH", "/channels/"+c.ID, body)
			return err
		})
	}
	_, hasAllow := c.Fields["allow"]
	_, hasDeny := c.Fields["deny"]
	if hasAllow || hasDeny {
		r.add(overwritePhase, fmt.Sprintf("set overwrites of %s %s", c.Kind, c.Name), func() error {
			overwrites, err := r.overwrites(entry.Overwrites())
			if err != nil {
				return err
			}
			_, err = r.call("PATCH", "/channels/"+c.ID, map[string]any{"permission_overwrites": overwrites})
			return err
		})
	}
}

// createBody includes the overwrites, so a private channel is never public even briefly.
func (r *run) createBody(entry folder.Entry) (map[string]any, error) {
	body := map[string]any{"name": entry.Name, "type": 4}
	overwrites := entry.Overwrites()
	if channel := entry.Channel; channel != nil {
		body["type"] = channelTypes[channel.Type]
		if entry.Parent != "" {
			if body["parent_id"] = r.ids[folder.Subject{Kind: folder.CategoryKind, Name: entry.Parent}]; body["parent_id"] == "" {
				return nil, fmt.Errorf("category %s has no Discord ID", entry.Parent)
			}
		}
		if channel.Topic != "" {
			body["topic"] = channel.Topic
		}
		if channel.Slowmode != "" {
			body["rate_limit_per_user"] = slowmodeSeconds(channel.Slowmode)
		}
		if len(channel.Tags) > 0 {
			tags := []map[string]string{}
			for _, tag := range channel.Tags {
				tags = append(tags, map[string]string{"name": tag})
			}
			body["available_tags"] = tags
		}
	}
	var err error
	body["permission_overwrites"], err = r.overwrites(overwrites)
	return body, err
}

// categoryID finds a category by the name the plan used for it: its name on the
// server, or its folder name if it was created in this run.
func (r *run) categoryID(name string) string {
	for _, entry := range r.live.Entries {
		if entry.Category != nil && entry.Name == name {
			return folder.EntryIdentity(entry).ID
		}
	}
	return r.ids[folder.Subject{Kind: folder.CategoryKind, Name: name}]
}

func (r *run) addChannelOrder(category folder.Entry) {
	var subjects []folder.Subject
	for _, name := range category.Category.Channels {
		subjects = append(subjects, folder.Subject{Kind: folder.ChannelKind, Name: category.Path + "/" + name})
	}
	r.addOrder("order the channels of "+category.Path, subjects)
}

func (r *run) addOrder(text string, subjects []folder.Subject) {
	if len(subjects) == 0 {
		return
	}
	r.add(orderPhase, text, func() error {
		positions := []map[string]any{}
		wanted := []string{}
		for position, subject := range subjects {
			if r.ids[subject] == "" {
				return fmt.Errorf("%s %s has no Discord ID", subject.Kind, subject.Name)
			}
			wanted = append(wanted, r.ids[subject])
			positions = append(positions, map[string]any{"id": r.ids[subject], "position": position})
		}
		if slices.Equal(wanted, r.liveOrder(wanted)) {
			return nil // already in this order; a rename can make the plan see a difference that is not there
		}
		_, err := r.call("PATCH", r.guildRoute("/channels"), positions)
		return err
	})
}

func (r *run) addServer(c change) {
	switch c.Name {
	case "settings":
		r.add(settingsPhase, c.String(), func() error { return r.changeSettings(c) })
	case "category order":
		var subjects []folder.Subject
		for _, name := range r.desired.Server.Categories {
			subjects = append(subjects, folder.Subject{Kind: folder.CategoryKind, Name: name})
		}
		r.addOrder("order the categories", subjects)
	case "role order":
		order, _ := c.Fields["value"].([]folder.RoleReference)
		r.add(roleOrderPhase, "set the role order", func() error { return r.changeRoleOrder(order) })
	}
}

func (r *run) changeSettings(c change) error {
	settings := r.desired.Server.Settings
	body := map[string]any{}
	if level := slices.Index(verification, settings.Verification); level >= 0 && c.Fields["verification"] != nil {
		body["verification_level"] = level
	}
	if level := slices.Index(notification, settings.Notifications); level >= 0 && c.Fields["notifications"] != nil {
		body["default_message_notifications"] = level
	}
	if _, changed := c.Fields["community"]; changed {
		if settings.Community == nil {
			r.byHand = append(r.byHand, "turn Community off in Discord; apply does not")
		} else {
			// The features list is replaced as a whole, so it is read first.
			current, err := r.target.Client.ReadConfiguration(r.ctx, r.target.Guild, "")
			if err != nil {
				return err
			}
			var guild struct {
				Features []string `json:"features"`
				Rules    string   `json:"rules_channel_id"`
				Updates  string   `json:"public_updates_channel_id"`
			}
			if err := json.Unmarshal(current.Body, &guild); err != nil {
				return err
			}
			rules, _ := r.channelByName(settings.Community.Rules)
			updates, _ := r.channelByName(settings.Community.Updates)
			if !slices.Contains(guild.Features, "COMMUNITY") || guild.Rules != rules || guild.Updates != updates {
				body["features"] = append(guild.Features, "COMMUNITY")
				body["rules_channel_id"], body["public_updates_channel_id"] = rules, updates
			}
		}
	}
	if len(body) == 0 {
		return nil
	}
	_, err := r.call("PATCH", r.guildRoute(""), body)
	return err
}

// changeRoleOrder moves only the roles the folder lists, among the positions they
// already occupy, so roles the folder does not list keep their places.
func (r *run) changeRoleOrder(order []folder.RoleReference) error {
	current, err := r.target.Client.ReadConfiguration(r.ctx, r.target.Guild, "/roles")
	if err != nil {
		return err
	}
	var live []struct {
		ID       string
		Position int
	}
	if err := json.Unmarshal(current.Body, &live); err != nil {
		return err
	}
	position := map[string]int{}
	for _, role := range live {
		position[role.ID] = role.Position
	}
	var wanted []string
	var slots []int
	for _, reference := range order {
		id := reference.ID
		if id == "" {
			id = r.ids[folder.Subject{Kind: folder.RoleKind, Name: reference.Name}]
		}
		if id != r.target.Guild {
			wanted = append(wanted, id)
			slots = append(slots, position[id])
		}
	}
	slices.SortFunc(slots, func(a, b int) int { return b - a })
	moves := []map[string]any{}
	for index, id := range wanted {
		if position[id] != slots[index] {
			moves = append(moves, map[string]any{"id": id, "position": slots[index]})
		}
	}
	if len(moves) == 0 {
		return nil
	}
	_, err = r.call("PATCH", r.guildRoute("/roles"), moves)
	return err
}

func (r *run) addAutoMod(c change) {
	var rule folder.AutoMod
	for _, candidate := range r.desired.Server.AutoMod {
		if candidate.Name == c.Name {
			rule = candidate
		}
	}
	creating := c.Operation == folder.Create
	has := func(fields ...string) bool {
		return creating || slices.ContainsFunc(fields, func(field string) bool { _, ok := c.Fields[field]; return ok })
	}
	r.add(autoModPhase, c.String(), func() error {
		body := map[string]any{}
		if has("name") {
			body["name"] = rule.Name
		}
		if has("keywords") {
			body["trigger_metadata"] = map[string]any{"keyword_filter": rule.Keywords}
		}
		if has("block", "alert") {
			actions := []map[string]any{}
			if rule.Block {
				actions = append(actions, map[string]any{"type": 1})
			}
			if rule.Alert != "" {
				channel, _ := r.channelByName(rule.Alert)
				actions = append(actions, map[string]any{"type": 2, "metadata": map[string]any{"channel_id": channel}})
			}
			body["actions"] = actions
		}
		if creating {
			body["event_type"], body["trigger_type"], body["enabled"] = 1, 1, true
			_, err := r.create(Owned{Kind: string(c.Kind), Path: c.Name}, r.guildRoute("/auto-moderation/rules"), body)
			return err
		}
		_, err := r.call("PATCH", r.guildRoute("/auto-moderation/rules/"+c.ID), body)
		return err
	})
}

// channelByName finds a channel (not a category) of the folder by its name.
func (r *run) channelByName(name string) (string, bool) {
	for _, entry := range r.desired.Entries {
		if entry.Category == nil && entry.Name == name {
			id := r.ids[folder.Subject{Kind: folder.ChannelKind, Name: entry.Path}]
			return id, id != ""
		}
	}
	return "", false
}

func (r *run) roleByName(name string) (string, bool) {
	if name == "everyone" {
		return r.target.Guild, true
	}
	id := r.ids[folder.Subject{Kind: folder.RoleKind, Name: name}]
	return id, id != ""
}

// addMessage posts or edits a standing message. The record is updated after each
// effect, so a failed pin is retried by the next run without posting again.
func (r *run) addMessage(c change) {
	channelPath, file := path.Split(c.Name)
	channelPath = strings.TrimSuffix(channelPath, "/")
	message := r.entries[channelPath].Messages[file]
	before, _ := c.Before.(folder.Message)
	r.add(messagePhase, c.String(), func() error {
		channelID := r.ids[folder.Subject{Kind: folder.ChannelKind, Name: channelPath}]
		payload, err := folder.RenderMessage(message, r.channelByName, r.roleByName)
		if err != nil {
			return err
		}
		pinned := before.Pin
		record := Owned{Kind: string(c.Kind), Path: c.Name, ID: c.ID, Channel: channelID, Content: recordedMessage(message, pinned)}
		if c.Operation == folder.Create {
			if record.ID, err = r.create(record, "/channels/"+channelID+"/messages", payload); err != nil {
				return err
			}
		} else {
			if _, err = r.call("PATCH", "/channels/"+channelID+"/messages/"+c.ID, payload); err != nil {
				return err
			}
			if err = r.target.Owned.Record(r.ctx, record); err != nil {
				return err
			}
		}
		if message.Pin == pinned {
			return nil
		}
		method := "DELETE"
		if message.Pin {
			method = "PUT"
		}
		if _, err = r.call(method, "/channels/"+channelID+"/messages/pins/"+record.ID, nil); err != nil {
			return err
		}
		record.Content = recordedMessage(message, message.Pin)
		return r.target.Owned.Record(r.ctx, record)
	})
}

// recordedMessage is what the plan later compares the file with.
func recordedMessage(message folder.Message, pinned bool) string {
	message.ID, message.Source, message.Pin = "", folder.Source{}, pinned
	data, _ := json.Marshal(message) // strings, a bool and valid JSON only
	return string(data)
}

// liveOrder sorts channel IDs by their place on the server, or returns nil if one
// of them is not on the server yet, so that a new channel is always out of order.
func (r *run) liveOrder(ids []string) []string {
	place := map[string]int{}
	for index, entry := range r.live.Entries {
		place[folder.EntryIdentity(entry).ID] = index
	}
	sorted := slices.Clone(ids)
	for _, id := range ids {
		if _, found := place[id]; !found {
			return nil
		}
	}
	slices.SortFunc(sorted, func(a, b string) int { return place[a] - place[b] })
	return sorted
}
