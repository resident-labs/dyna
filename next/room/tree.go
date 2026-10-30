package room

import (
	"errors"
	"github.com/resident-labs/dyna/contract"
)

// Node contains the tree facts loaded by the engine.
type Node struct {
	ID     contract.SessionID
	Parent contract.SessionID
}

// Tree represents one room's session tree.
type Tree struct {
	nodes map[contract.SessionID]Node
	root  contract.SessionID
}

func NewTree(nodes []Node) Tree {
	t := Tree{nodes: make(map[contract.SessionID]Node, len(nodes))}
	for _, n := range nodes {
		t.nodes[n.ID] = n
		if n.Parent == "" {
			t.root = n.ID
		}
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
