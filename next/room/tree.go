package room

import (
	"errors"
	"sort"

	"github.com/resident-labs/dyna/contract"
)

// Node contains the tree facts loaded by the engine.
type Node struct {
	ID     contract.SessionID
	Parent contract.SessionID
	Room   contract.RoomID
	Host   contract.HostName
}

// Tree represents one room's session tree.
type Tree struct {
	nodes map[contract.SessionID]Node
	root  contract.SessionID
}

func NewTree(nodes []Node) Tree {
	t := Tree{nodes: make(map[contract.SessionID]Node, len(nodes))}
	for _, n := range nodes {
		// M1 trial stores can have several parentless sessions; attach later ones to root.
		if n.Parent == "" {
			if t.root == "" {
				t.root = n.ID
			} else {
				n.Parent = t.root
			}
		}
		t.nodes[n.ID] = n
	}
	return t
}

// ParentFor picks the caller for a session start, or the requested parent
// for an owner start, defaulting to root.
func (t Tree) ParentFor(caller contract.Principal, requested contract.SessionID) (contract.SessionID, error) {
	if caller.Kind == contract.PrincipalSession {
		return caller.Session, nil
	}
	if requested == "" {
		requested = t.root
	}
	if _, ok := t.nodes[requested]; !ok {
		return "", errors.New("parent is not in the room tree")
	}
	return requested, nil
}

// Above reports strict ancestry; a session is not above itself.
func (t Tree) Above(a, b contract.SessionID) bool {
	for n, ok := t.nodes[b]; ok && n.Parent != ""; n, ok = t.nodes[n.Parent] {
		if n.Parent == a {
			return true
		}
	}
	return false
}

func (t Tree) CanManage(caller contract.Principal, target contract.SessionID) bool {
	_, exists := t.nodes[target]
	return exists && (caller.Kind == contract.PrincipalOwner || caller.Kind == contract.PrincipalSession && t.Above(caller.Session, target))
}

func (t Tree) HoldsGoals(s contract.SessionID) bool {
	n, exists := t.nodes[s]
	return exists && n.Parent != ""
}

// CloseOrder returns a subtree deepest first, with a stable order at each depth.
func (t Tree) CloseOrder(s contract.SessionID) ([]contract.SessionID, error) {
	if _, ok := t.nodes[s]; !ok {
		return nil, errors.New("session is not in the room tree")
	}
	if s == t.root {
		return nil, errors.New("root cannot close")
	}
	type member struct {
		id    contract.SessionID
		depth int
	}
	var members []member
	for id := range t.nodes {
		depth := 0
		for n := t.nodes[id]; n.ID != s && n.Parent != ""; n = t.nodes[n.Parent] {
			depth++
		}
		if id == s || t.Above(s, id) {
			members = append(members, member{id, depth})
		}
	}
	sort.Slice(members, func(i, j int) bool {
		if members[i].depth != members[j].depth {
			return members[i].depth > members[j].depth
		}
		return members[i].id < members[j].id
	})
	out := make([]contract.SessionID, 0, len(members))
	for _, m := range members {
		out = append(out, m.id)
	}
	return out, nil
}

type Handoff struct {
	PRHolder   contract.SessionID
	GoalHolder contract.SessionID
}

// Handoff returns the parent for PRs and the parent for goals, unless it is root.
func (t Tree) Handoff(s contract.SessionID) (Handoff, error) {
	n, ok := t.nodes[s]
	if !ok || n.Parent == "" {
		return Handoff{}, errors.New("root or unknown session cannot close")
	}
	h := Handoff{PRHolder: n.Parent}
	if n.Parent != t.root {
		h.GoalHolder = n.Parent
	}
	return h, nil
}

// CheckMove rejects moving root, cycles, another room, and another host.
func (t Tree) CheckMove(s contract.SessionID, parent Node) error {
	n, ok := t.nodes[s]
	if !ok || s == t.root {
		return errors.New("root or unknown session cannot move")
	}
	if parent.Room != n.Room {
		return errors.New("parent is in another room")
	}
	if _, ok := t.nodes[parent.ID]; !ok {
		return errors.New("parent is not in the room tree")
	}
	if parent.ID == s || t.Above(s, parent.ID) {
		return errors.New("move would make a cycle")
	}
	if n.Host != parent.Host {
		return errors.New("parent is on another host")
	}
	return nil
}
