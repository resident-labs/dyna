package room

import (
	"errors"
	"sort"
	"strings"

	"github.com/resident-labs/dyna/contract"
)

type AddressKind string

const (
	AddressName     AddressKind = "name"
	AddressParent   AddressKind = "parent"
	AddressChildren AddressKind = "children"
	AddressSiblings AddressKind = "siblings"
	AddressRoot     AddressKind = "root"
)

type Address struct {
	Kind AddressKind
	Room contract.RoomName
	Name string
}

func ParseAddress(s string) (Address, error) {
	if !strings.HasPrefix(s, "@") || len(s) < 2 {
		return Address{}, errors.New("invalid address")
	}
	s = s[1:]
	if strings.ContainsAny(s, " \t\n@") {
		return Address{}, errors.New("invalid address")
	}
	if room, name, ok := strings.Cut(s, ":"); ok {
		if room == "" || name == "" || strings.Contains(name, ":") {
			return Address{}, errors.New("invalid address")
		}
		return Address{Kind: AddressName, Room: contract.RoomName(room), Name: name}, nil
	}
	switch s {
	case "parent":
		return Address{Kind: AddressParent}, nil
	case "children":
		return Address{Kind: AddressChildren}, nil
	case "siblings":
		return Address{Kind: AddressSiblings}, nil
	case "root":
		return Address{Kind: AddressRoot}, nil
	default:
		return Address{Kind: AddressName, Name: s}, nil
	}
}

// Resolve uses names loaded by the engine for the address's room.
func (t Tree) Resolve(from contract.SessionID, address Address, names map[string]contract.SessionID) ([]contract.SessionID, bool) {
	group := address.Kind == AddressChildren || address.Kind == AddressSiblings
	var result []contract.SessionID
	switch address.Kind {
	case AddressName:
		if id := names[address.Name]; id != "" {
			result = append(result, id)
		}
	case AddressRoot:
		if t.root != "" {
			result = append(result, t.root)
		}
	case AddressParent:
		if n, ok := t.nodes[from]; ok && n.Parent != "" {
			result = append(result, n.Parent)
		}
	case AddressChildren, AddressSiblings:
		n, ok := t.nodes[from]
		if !ok {
			break
		}
		for id, node := range t.nodes {
			if id != from && ((address.Kind == AddressChildren && node.Parent == from) || (address.Kind == AddressSiblings && n.Parent != "" && node.Parent == n.Parent)) {
				result = append(result, id)
			}
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, group
}
