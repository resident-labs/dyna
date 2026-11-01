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
		// LEGACY(v1-transfer): attaching extra parentless sessions to root, for M1 trial stores. Remove after the transfer.
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

// Root returns the room root, or an empty ID for an empty tree.
func (t Tree) Root() contract.SessionID { return t.root }

// Depth returns the depth of a session, with root at 1.
func (t Tree) Depth(id contract.SessionID) int {
	depth := 0
	for n, ok := t.nodes[id]; ok; n, ok = t.nodes[n.Parent] {
		depth++
	}
	return depth
}

// ParentFor picks the caller for a session start, or the requested parent
// for an owner start, defaulting to root.
func (t Tree) ParentFor(caller contract.Principal, requested contract.SessionID) (contract.SessionID, error) {
	if t.root == "" {
		return "", errors.New("room has no root")
	}
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

// CanTend reports whether caller may work on a goal held by holder: the owner, the holder, anyone above it, and root for a goal with no holder.
func (t Tree) CanTend(caller contract.Principal, holder contract.SessionID) bool {
	switch {
	case caller.Kind == contract.PrincipalOwner:
		return true
	case caller.Kind != contract.PrincipalSession:
		return false
	case holder == "":
		return caller.Session == t.root
	}
	return caller.Session == holder || t.Above(caller.Session, holder)
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

// Leaves selects sessions without open children, in the supplied close order.
func (t Tree) Leaves(order []contract.SessionID) []contract.SessionID {
	parents := make(map[contract.SessionID]bool)
	for _, n := range t.nodes {
		parents[n.Parent] = true
	}
	var leaves []contract.SessionID
	for _, id := range order {
		if !parents[id] {
			leaves = append(leaves, id)
		}
	}
	return leaves
}

// CheckMove rejects moving root, cycles and another room.
func (t Tree) CheckMove(s, parent contract.SessionID) error {
	_, ok := t.nodes[s]
	if !ok || s == t.root {
		return errors.New("root or unknown session cannot move")
	}
	_, ok = t.nodes[parent]
	if !ok {
		return errors.New("parent is not in the room tree")
	}
	if parent == s || t.Above(s, parent) {
		return errors.New("move would make a cycle")
	}
	return nil
}
