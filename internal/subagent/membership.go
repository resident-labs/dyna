package subagent

import "sort"

// MemberNode is a session and its visible children in a room's member tree.
type MemberNode struct {
	Info     Info
	Children []*MemberNode
}

// Membership contains the creation-ordered sessions and their parent tree.
type Membership struct {
	Sessions []Info
	Roots    []*MemberNode
}

// RoomMembership filters sessions and orders siblings by creation time and ID.
// Missing parents become roots. Cycles appear once, with the repeated edge cut.
func RoomMembership(all []Info, roomID string, withClosed bool) Membership {
	var out Membership
	byID := map[string]bool{}
	for _, info := range all {
		if info.RoomID == roomID && (withClosed || info.Status != StatusClosed) {
			out.Sessions = append(out.Sessions, info)
			byID[info.ID] = true
		}
	}
	sort.SliceStable(out.Sessions, func(i, j int) bool {
		a, b := out.Sessions[i], out.Sessions[j]
		if !a.Created.Equal(b.Created) {
			return a.Created.Before(b.Created)
		}
		return a.ID < b.ID
	})
	children := map[string][]Info{}
	for _, info := range out.Sessions {
		parent := info.ParentSession
		if !byID[parent] {
			parent = ""
		}
		children[parent] = append(children[parent], info)
	}
	visited := map[string]bool{}
	var build func(Info) *MemberNode
	build = func(info Info) *MemberNode {
		if visited[info.ID] {
			return nil
		}
		visited[info.ID] = true
		node := &MemberNode{Info: info}
		for _, child := range children[info.ID] {
			if n := build(child); n != nil {
				node.Children = append(node.Children, n)
			}
		}
		return node
	}
	for _, info := range children[""] {
		if n := build(info); n != nil {
			out.Roots = append(out.Roots, n)
		}
	}
	for _, info := range out.Sessions {
		if n := build(info); n != nil {
			out.Roots = append(out.Roots, n)
		}
	}
	return out
}
