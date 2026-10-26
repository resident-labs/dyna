package notify

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/room"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/subagent"
)

// Selector names. They and the removed "room" and "owner" are reserved: no
// agent may use them as a name (room.ValidateAgent).
const (
	selRoot     = "root"
	selTeam     = "team"
	selParent   = "parent"
	selSiblings = "siblings"
	selChildren = "children"
)

// removedSelectors were owner mentions before @root. They fail with a hint.
var removedSelectors = map[string]bool{"room": true, "owner": true}

// resolved is the outcome of mention resolution: one room, the sender's
// address in it, and the deduplicated recipients.
type resolved struct {
	rec        room.Record
	from       string
	via        []string
	recipients []recipient
	skipped    []Skip
}

func (r resolved) addrs() []string {
	out := make([]string, len(r.recipients))
	for i, rc := range r.recipients {
		out[i] = rc.addr
	}
	return out
}

// PreviewResult is a routing decision without storage, repair, or delivery.
type PreviewResult struct {
	Recipients []string `json:"recipients"`
	Skipped    []Skip   `json:"skipped,omitempty"`
}

// Preview applies the same mention and eligibility rules as Post.
func (s *Service) Preview(snd Sender, mentions []string) (PreviewResult, error) {
	res, err := s.resolveMode(snd, mentions, true, false)
	if err != nil {
		return PreviewResult{}, err
	}
	return PreviewResult{Recipients: res.addrs(), Skipped: res.skipped}, nil
}

// ResolveSessions applies Post's mention rules and returns only session recipients.
// It allows explicit self assignment, is read-only, and rejects non-session recipients.
func (s *Service) ResolveSessions(snd Sender, mentions []string) ([]subagent.Info, error) {
	res, err := s.resolveMode(snd, mentions, true, true)
	if err != nil {
		return nil, err
	}
	out := make([]subagent.Info, 0, len(res.recipients))
	for _, rc := range res.recipients {
		if rc.kind != toSession {
			return nil, fmt.Errorf("%s is not a session", rc.addr)
		}
		out = append(out, rc.session)
	}
	return out, nil
}

type resolver struct {
	readOnly  bool
	allowSelf bool
	s         *Service
	snd       Sender
	sessions  []subagent.Info
	loaded    bool
}

func (r *resolver) open() ([]subagent.Info, error) {
	if r.loaded {
		return r.sessions, nil
	}
	sessions, err := r.list(false)
	if err != nil {
		return nil, err
	}
	r.sessions, r.loaded = sessions, true
	return sessions, nil
}

func (r *resolver) list(all bool) ([]subagent.Info, error) {
	if r.readOnly {
		return r.s.sessionStore().ListSnapshot(all)
	}
	return r.s.sessionStore().List(all)
}

func (r *resolver) room(ref string) (room.Record, error) {
	return r.s.Rooms.Resolve(ref)
}

func (r *resolver) senderAddress(rec room.Record) string {
	switch r.snd.Kind {
	case SenderSession:
		return r.snd.Session.Address()
	case SenderScript:
		return "run:" + r.snd.RunID
	case SenderVoice:
		return rec.Name + ":" + room.VoiceAgent
	}
	return rec.Name
}

// usable reports why a session cannot take a post, or "". Recovery paths
// (a root seat, a parent fallback, a group member choice) use it to decide
// whether to replace or skip a session, so a missing harness conversation
// stays a refusal there.
func usable(info subagent.Info) string {
	switch {
	case info.Status == subagent.StatusClosed:
		return "closed"
	case !info.Resumable:
		return "not resumable (its harness has no exact-session resume)"
	case missingHarnessSession(info):
		return "has no harness session to resume yet"
	}
	return ""
}

// missingHarnessSession reports a session whose first turn ended before the
// harness reported a session. Its next turn opens a fresh conversation.
func missingHarnessSession(info subagent.Info) bool {
	return (info.Status == subagent.StatusIdle || info.Status == subagent.StatusDormant) && info.HarnessSession == ""
}

// takesPost reports why a named recipient cannot take a post. A resumable
// session whose first turn ran but never recorded a harness conversation still
// takes one, and the turn that follows starts fresh. A session that never
// started a turn is still being created, and every other case keeps the strict
// rule.
func takesPost(info subagent.Info) string {
	if info.Resumable && info.Turns > 0 && missingHarnessSession(info) {
		return ""
	}
	return usable(info)
}

// resolve turns mention tokens into one room and its recipients. Any error
// here means nothing is stored.
func (s *Service) resolve(snd Sender, mentions []string) (resolved, error) {
	return s.resolveMode(snd, mentions, false, false)
}

func (s *Service) resolveMode(snd Sender, mentions []string, readOnly, allowSelf bool) (resolved, error) {
	if len(mentions) == 0 {
		return resolved{}, errors.New("a post needs at least one @mention; mention @root to post to the room owner")
	}
	r := &resolver{s: s, snd: snd, readOnly: readOnly, allowSelf: allowSelf}
	var out resolved
	seen := map[string]bool{}
	var roomOf string // room name of the first recipient
	var firstTok string
	for _, tok := range mentions {
		recips, skipped, err := r.token(tok)
		if err != nil {
			return resolved{}, err
		}
		for _, rc := range recips {
			if roomOf == "" {
				roomOf, firstTok, out.rec = rc.rec.Name, tok, rc.rec
			} else if rc.rec.ID != out.rec.ID {
				return resolved{}, fmt.Errorf("a post goes to one room: %s is in %s but %s is in %s; split it into one post per room", firstTok, roomOf, tok, rc.rec.Name)
			}
			if seen[rc.addr] {
				continue
			}
			seen[rc.addr] = true
			out.recipients = append(out.recipients, rc)
		}
		out.skipped = append(out.skipped, skipped...)
		out.via = append(out.via, tok)
	}
	out.from = r.senderAddress(out.rec)
	return out, nil
}

// token resolves one mention token.
func (r *resolver) token(tok string) ([]recipient, []Skip, error) {
	if !strings.HasPrefix(tok, "@") || len(tok) < 2 {
		return nil, nil, fmt.Errorf("invalid mention %q: mentions start with @", tok)
	}
	body := tok[1:]
	if strings.HasPrefix(body, "run:") {
		rc, err := r.worker(tok, body)
		if err != nil {
			return nil, nil, err
		}
		return []recipient{rc}, nil, nil
	}
	roomName, local, qualified := strings.Cut(body, ":")
	if !qualified {
		roomName, local = "", body
	}
	if strings.Contains(local, ":") {
		return nil, nil, fmt.Errorf("invalid mention %q", tok)
	}
	if !qualified {
		switch local {
		case selRoot, selTeam, selParent, selSiblings, selChildren, room.VoiceAgent, "room", "owner":
			if r.snd.Room == "" {
				return nil, nil, fmt.Errorf("%s: no room context; write @<room>:%s", tok, local)
			}
			roomName = r.snd.Room
		default:
			// A bare name is a member of the sender's room when one is open
			// there; otherwise it names a room, meaning that room's owner.
			if r.snd.Room != "" {
				if info, ok, err := r.member(r.snd.Room, local); err != nil {
					return nil, nil, err
				} else if ok {
					rc, err := r.named(tok, info)
					return []recipient{rc}, nil, err
				}
			}
			if room.ValidateName(local) == nil {
				if rec, err := r.room(local); err == nil {
					return []recipient{{kind: toOwner, addr: rec.Name, rec: rec}}, nil, nil
				}
			}
			if r.snd.Room == "" {
				return nil, nil, fmt.Errorf("%s: no room context; write @<room>:%s", tok, local)
			}
			return nil, nil, fmt.Errorf("%s: no open session named %q in room %s", tok, local, r.snd.Room)
		}
	}
	rec, err := r.room(roomName)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", tok, err)
	}
	if removedSelectors[local] {
		return nil, nil, fmt.Errorf("%s was removed: mention @root for the room owner, or @team for the owner's top-level agents", tok)
	}
	switch local {
	case "", selRoot:
		return []recipient{{kind: toOwner, addr: rec.Name, rec: rec}}, nil, nil
	case room.VoiceAgent:
		if err := authority.Check("post @voice-call", senderActor(r.snd), nil); err != nil {
			return nil, nil, err
		}
		return []recipient{{kind: toVoice, addr: rec.Name + ":" + room.VoiceAgent, rec: rec}}, nil, nil
	case selParent:
		rc, err := r.parent(tok, rec)
		if err != nil {
			return nil, nil, err
		}
		return []recipient{rc}, nil, nil
	case selTeam, selSiblings, selChildren:
		return r.group(tok, local, rec)
	}
	if err := room.ValidateAgent(local); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", tok, err)
	}
	info, ok, err := r.member(rec.Name, local)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return nil, nil, fmt.Errorf("%s: no open session named %q in room %s", tok, local, rec.Name)
	}
	rc, err := r.named(tok, info)
	if err != nil {
		return nil, nil, err
	}
	return []recipient{rc}, nil, nil
}

func (r *resolver) member(roomName, name string) (subagent.Info, bool, error) {
	sessions, err := r.open()
	if err != nil {
		return subagent.Info{}, false, err
	}
	for _, info := range sessions {
		if info.Room == roomName && info.EffectiveName() == name {
			return info, true, nil
		}
	}
	// Closed consumed names remain delivery aliases; exact archive reads still use Resolve.
	all, err := r.list(true)
	if err != nil {
		return subagent.Info{}, false, err
	}
	for _, info := range all {
		if info.Room == roomName && info.EffectiveName() == name && info.ConsumedBy != "" {
			var target subagent.Info
			var err error
			if r.readOnly {
				target, err = r.s.sessionStore().InspectDeliveryTarget(info.ID)
			} else {
				target, err = r.s.sessionStore().DeliveryTarget(info.ID)
			}
			return target, err == nil, err
		}
	}
	return subagent.Info{}, false, nil
}

func (r *resolver) self(info subagent.Info) bool {
	return r.snd.Kind == SenderSession && info.ID == r.snd.Session.ID
}

// named checks an explicitly named session. Any problem rejects the post.
func (r *resolver) named(tok string, info subagent.Info) (recipient, error) {
	if r.self(info) && !r.allowSelf {
		return recipient{}, fmt.Errorf("%s is you; a post cannot mention its sender", tok)
	}
	if why := takesPost(info); why != "" {
		return recipient{}, fmt.Errorf("%s (%s) is %s", tok, info.Address(), why)
	}
	return r.sessionRecipient(info)
}

func (r *resolver) sessionRecipient(info subagent.Info) (recipient, error) {
	rec, err := r.room(info.RoomID)
	if err != nil {
		return recipient{}, err
	}
	return recipient{kind: toSession, addr: info.Address(), rec: rec, session: info}, nil
}

func (r *resolver) ownRoom(tok string, rec room.Record) error {
	if r.snd.Room == "" || rec.Name != r.snd.Room {
		return fmt.Errorf("%s: this selector only works in your own room", tok)
	}
	return nil
}

func (r *resolver) parent(tok string, rec room.Record) (recipient, error) {
	if r.snd.Kind == SenderOwner {
		return recipient{}, fmt.Errorf("%s: the room owner has no parent", tok)
	}
	if err := r.ownRoom(tok, rec); err != nil {
		return recipient{}, err
	}
	owner := recipient{kind: toOwner, addr: rec.Name, rec: rec}
	var parentID string
	switch r.snd.Kind {
	case SenderSession:
		parentID = r.snd.Session.ParentSession
	case SenderScript:
		meta, err := runstore.ReadMeta(r.snd.RunID)
		if err != nil {
			return recipient{}, err
		}
		parentID = meta.ParentSession
	}
	if parentID == "" {
		return owner, nil
	}
	var parent subagent.Info
	var err error
	if r.readOnly {
		parent, err = r.s.sessionStore().Inspect(parentID)
	} else {
		parent, err = r.s.sessionStore().Info(parentID)
	}
	if err != nil {
		return recipient{}, fmt.Errorf("%s: %w", tok, err)
	}
	if r.snd.Kind != SenderSession && parent.Status == subagent.StatusClosed {
		return owner, nil
	}
	// A parent that is the room's root is the seat itself: @parent names the
	// room's own address, exactly as @root does, whether the internal root or an
	// external orchestrator holds the seat. The seat's delivery row carries the
	// wake decision (room.RootContext), and a session's message to the seat wakes
	// root.
	if parent.Role == subagent.RoleRoot && parent.RoomID == rec.ID {
		return owner, nil
	}
	if why := usable(parent); why != "" {
		return recipient{}, fmt.Errorf("%s (%s) is %s; mention @root instead", tok, parent.Address(), why)
	}
	return r.sessionRecipient(parent)
}

// group resolves @team, @siblings or @children to open task sessions. Members that
// can never take a post are skipped and reported, not failed.
func (r *resolver) group(tok, sel string, rec room.Record) ([]recipient, []Skip, error) {
	var match func(subagent.Info) bool
	switch {
	case sel == selTeam && r.snd.Kind != SenderOwner:
		return nil, nil, fmt.Errorf("%s: only the room owner can use @team; a session uses @parent, @siblings or @children", tok)
	case r.snd.Kind == SenderSession:
		if err := r.ownRoom(tok, rec); err != nil {
			return nil, nil, err
		}
		me := r.snd.Session
		if sel == selSiblings {
			match = func(info subagent.Info) bool {
				return info.ID != me.ID && info.RoomID == me.RoomID && info.ParentSession == me.ParentSession
			}
		} else {
			match = func(info subagent.Info) bool { return info.ParentSession == me.ID }
		}
	case r.snd.Kind == SenderOwner && sel == selTeam:
		match = func(info subagent.Info) bool { return info.RoomID == rec.ID && info.ParentSession == "" }
	case r.snd.Kind == SenderOwner:
		return nil, nil, fmt.Errorf("%s: the room owner reaches its top-level agents with @team", tok)
	default:
		return nil, nil, fmt.Errorf("%s: only a session can use @siblings or @children", tok)
	}
	sessions, err := r.open()
	if err != nil {
		return nil, nil, err
	}
	var out []recipient
	var skipped []Skip
	for _, info := range sessions {
		// A parent's crew assignment is service work, even when it uses a group.
		if !match(info) || info.RunOwned() || (info.Service && sel != selChildren) {
			continue
		}
		if why := usable(info); why != "" {
			skipped = append(skipped, Skip{To: info.Address(), Reason: why})
			continue
		}
		rc, err := r.sessionRecipient(info)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, rc)
	}
	if len(out) == 0 {
		return nil, nil, fmt.Errorf("%s resolved to nobody who can take a post", tok)
	}
	return out, skipped, nil
}

// worker resolves @run:<id>/agent:<n> to the leaf session that serves that
// agent call, which takes the post as any session does.
func (r *resolver) worker(tok, body string) (recipient, error) {
	parts := strings.Split(strings.TrimPrefix(body, "run:"), "/agent:")
	if len(parts) != 2 {
		return recipient{}, fmt.Errorf("workflow agent mention must be @run:<run-id>/agent:<n>, got %q", tok)
	}
	agent, err := strconv.Atoi(parts[1])
	if err != nil || agent < 1 {
		return recipient{}, fmt.Errorf("invalid workflow agent mention %q", tok)
	}
	info, err := r.s.sessionStore().Inspect(subagent.RunAgentID(parts[0], agent))
	if err != nil || info.Status == subagent.StatusClosed {
		return recipient{}, fmt.Errorf("%s: agent %d of run %s is not running", tok, agent, parts[0])
	}
	return r.sessionRecipient(info)
}
