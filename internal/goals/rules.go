package goals

import (
	"crypto/sha256"
	"dyna-agent/internal/room"
	"dyna-agent/internal/settings"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxRules = 20
const MaxRuleText = 500

type Rule struct {
	ID         string    `json:"id"`
	Text       string    `json:"text"`
	Setter     string    `json:"setter"`
	SetterName string    `json:"setterName"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
	Edits      []Edit    `json:"edits,omitempty"`
}

type View struct {
	Rules []Rule `json:"rules"`
	Used  int    `json:"used"`
	Max   int    `json:"max"`
	Hash  string `json:"hash"`
}

// Global is the scope key for rules that apply in every room. Room IDs never
// equal it. Global rule IDs use the "u" prefix; room rule IDs use "r".
const Global = "*"

// rules returns the rule set of a scope, or nil when it has none yet.
func (st *State) rules(scope string) *RuleSet {
	if scope == Global {
		return st.Global
	}
	if r := st.Rooms[scope]; r != nil {
		return &r.RuleSet
	}
	return nil
}

func (s *Store) Rules(scope string) ([]Rule, error) {
	st, err := s.read(false, noGoals)
	if err != nil {
		return nil, err
	}
	out := []Rule{}
	if r := st.rules(scope); r != nil {
		for _, rule := range r.Rules {
			out = append(out, *rule)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := strconv.Atoi(strings.TrimLeft(out[i].ID, "ru"))
		b, _ := strconv.Atoi(strings.TrimLeft(out[j].ID, "ru"))
		return a < b
	})
	return out, nil
}

// rulesLimit is the one place that reads the owner's rule text cap. Every
// store reads the same settings file, so room, global and instruction paths agree.
func (s *Store) rulesLimit() (int, error) {
	v, err := settings.Load(settings.DefaultPath())
	return v.RulesMaxChars, err
}

func (s *Store) RulesView(scope string) (View, error) {
	rules, err := s.Rules(scope)
	if err != nil {
		return View{}, err
	}
	limit, err := s.rulesLimit()
	if err != nil {
		return View{}, err
	}
	v := View{Rules: rules, Max: limit}
	pairs := make([][2]string, 0, len(rules))
	for _, rule := range rules {
		v.Used += utf8.RuneCountInString(rule.Text)
		pairs = append(pairs, [2]string{rule.ID, rule.Text})
	}
	b, _ := json.Marshal(pairs)
	sum := sha256.Sum256(b)
	v.Hash = fmt.Sprintf("%x", sum[:8])
	return v, nil
}

func ruleText(text string) (string, error) {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return "", fmt.Errorf("rule text is required")
	}
	if utf8.RuneCountInString(text) > MaxRuleText {
		return "", fmt.Errorf("rule text exceeds 500 characters")
	}
	return text, nil
}

func ruleCapacity(r *RuleSet, id, text string, limit int) error {
	used := 0
	for _, rule := range r.Rules {
		used += utf8.RuneCountInString(rule.Text)
	}
	next := used + utf8.RuneCountInString(text)
	if old := r.Rules[id]; old != nil {
		next -= utf8.RuneCountInString(old.Text)
	}
	if next > limit {
		return fmt.Errorf("Rules are full (%s / %s characters). Shorten or remove a rule first.", ruleNumber(used), ruleNumber(limit))
	}
	if id == "" && len(r.Rules) >= MaxRules {
		return fmt.Errorf("this scope has 20 rules. Shorten or remove a rule first.")
	}
	return nil
}

func ruleNumber(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%d %03d", n/1000, n%1000)
	}
	return strconv.Itoa(n)
}

func (s *Store) AddRule(scope, by, byName, text string) (Rule, error) {
	var out Rule
	if scope == "" {
		return out, fmt.Errorf("a room is required")
	}
	text, err := ruleText(text)
	if err != nil {
		return out, err
	}
	limit, err := s.rulesLimit()
	if err != nil {
		return out, err
	}
	err = s.UpdateOpen(func(st *State) error {
		prefix := "r"
		r := st.rules(scope)
		if scope == Global {
			prefix = "u"
			if r == nil {
				st.Global = &RuleSet{}
				r = st.Global
			}
		} else if r == nil {
			r = &st.Room(scope).RuleSet
		}
		if err := ruleCapacity(r, "", text, limit); err != nil {
			return err
		}
		if r.Rules == nil {
			r.Rules = map[string]*Rule{}
		}
		r.NextRuleID++
		now := s.now()
		out = Rule{ID: fmt.Sprintf("%s%d", prefix, r.NextRuleID), Text: text, Setter: by, SetterName: byName, CreatedAt: now, UpdatedAt: now}
		r.Rules[out.ID] = &out
		return nil
	})
	return out, err
}

// EditRule checks authorizeAs for permission and records by in the edit history.
func (s *Store) EditRule(scope, id, by, authorizeAs, text string) (Rule, error) {
	limit, err := s.rulesLimit()
	if err != nil {
		return Rule{}, err
	}
	return s.changeRule(scope, id, authorizeAs, func(r *RuleSet, rule *Rule) error {
		text, err := ruleText(text)
		if err != nil {
			return err
		}
		if err := ruleCapacity(r, id, text, limit); err != nil {
			return err
		}
		rule.Edits = append(rule.Edits, Edit{At: s.now(), By: by, OldText: rule.Text})
		rule.Text, rule.UpdatedAt = text, s.now()
		return nil
	})
}

func (s *Store) RemoveRule(scope, id, by string) (Rule, error) {
	return s.changeRule(scope, id, by, func(r *RuleSet, rule *Rule) error {
		delete(r.Rules, id)
		return nil
	})
}

func (s *Store) changeRule(scope, id, by string, fn func(*RuleSet, *Rule) error) (Rule, error) {
	var out Rule
	err := s.UpdateOpen(func(st *State) error {
		r := st.rules(scope)
		if r == nil || r.Rules[id] == nil {
			if scope == Global {
				return fmt.Errorf("global rule %s does not exist", id)
			}
			return fmt.Errorf("rule %s does not exist in this room", id)
		}
		rule := r.Rules[id]
		if by != "owner" && rule.Setter == "owner" {
			return fmt.Errorf("%s was written by the owner. Ask the owner to change it.", id)
		}
		if err := fn(r, rule); err != nil {
			return err
		}
		out = *rule
		return nil
	})
	return out, err
}

// PostRuleNotice records a rule change and the sessions its instruction note
// reaches. It stays one owner row instead of echoing every session's note.
func PostRuleNotice(rooms *room.Store, rec room.Record, rule Rule, who, verb string, sessions []string) error {
	scope := "room"
	if strings.HasPrefix(rule.ID, "u") {
		scope = "global"
	}
	ev := room.RuleEvent{RuleID: rule.ID, Scope: scope, Actor: who, Verb: room.RuleVerb(verb), Text: rule.Text, Sessions: sessions}
	_, err := rooms.PostEventInput(room.PostEventInput{RoomID: rec.ID, Event: ev, To: []string{rec.Name}})
	return err
}
