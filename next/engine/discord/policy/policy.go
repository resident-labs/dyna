// Package policy decodes the closed vocabulary of Discord front policies.
package policy

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Switch bool

func (s *Switch) UnmarshalYAML(n *yaml.Node) error {
	switch n.Value {
	case "yes", "true":
		*s = true
	case "no", "false":
		*s = false
	default:
		return fmt.Errorf("line %d: expected yes or no", n.Line)
	}
	return nil
}

type Policy struct {
	Enabled *bool  `yaml:"enabled" json:"enabled,omitempty"`
	Log     string `yaml:"log" json:"log,omitempty"`
	Front   Front  `yaml:"front" json:"front"`
}
type Front struct {
	TimeoutMax string   `yaml:"timeout_max" json:"timeout_max,omitempty"`
	Roles      []string `yaml:"roles" json:"roles,omitempty"`
	Delete     Switch   `yaml:"delete" json:"delete"`
	Threads    Switch   `yaml:"threads" json:"threads"`
	Quiet      []string `yaml:"quiet" json:"quiet,omitempty"`
	Answers    Answers  `yaml:"answers" json:"answers"`
}
type Answers struct {
	PerMember  string `yaml:"per_member" json:"per_member,omitempty"`
	PerChannel string `yaml:"per_channel" json:"per_channel,omitempty"`
}

func (p Policy) On() bool { return p.Enabled == nil || *p.Enabled }
func Budget(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(strings.TrimSuffix(s, "/h"))
	if err != nil || n < 1 || n > 100000 || !strings.HasSuffix(s, "/h") {
		return 0, fmt.Errorf("answer budget must be N/h (1 to 100000)")
	}
	return n, nil
}
func Decode(text string) (Policy, error) {
	var p Policy
	if len(text) > 65536 {
		return p, fmt.Errorf("policy exceeds 64 KB")
	}
	var node yaml.Node
	d := yaml.NewDecoder(strings.NewReader(text))
	if err := d.Decode(&node); err != nil {
		return p, err
	}
	var extra yaml.Node
	if err := d.Decode(&extra); err != io.EOF {
		return p, fmt.Errorf("policy must contain one YAML document")
	}
	var inspect func(*yaml.Node) error
	inspect = func(n *yaml.Node) error {
		if n.Kind == yaml.AliasNode || n.Anchor != "" {
			return fmt.Errorf("line %d: aliases and anchors are refused", n.Line)
		}
		if n.Kind == yaml.MappingNode {
			seen := map[string]bool{}
			for i := 0; i < len(n.Content); i += 2 {
				k := n.Content[i]
				if seen[k.Value] {
					return fmt.Errorf("line %d: duplicate key %s", k.Line, k.Value)
				}
				seen[k.Value] = true
			}
		}
		for _, c := range n.Content {
			if err := inspect(c); err != nil {
				return err
			}
		}
		return nil
	}
	if err := inspect(&node); err != nil {
		return p, err
	}
	if len(node.Content) == 0 || node.Content[0].Kind != yaml.MappingNode {
		return p, fmt.Errorf("policy must be a mapping")
	}
	for i := 0; i < len(node.Content[0].Content); i += 2 {
		k := node.Content[0].Content[i]
		switch k.Value {
		case "enabled", "log", "front":
		default:
			return p, fmt.Errorf("line %d: %s not supported yet", k.Line, k.Value)
		}
	}
	strict := yaml.NewDecoder(bytes.NewBufferString(text))
	strict.KnownFields(true)
	if err := strict.Decode(&p); err != nil {
		return p, err
	}
	if p.Front.TimeoutMax != "" {
		v, e := time.ParseDuration(p.Front.TimeoutMax)
		if e != nil || v < 0 || v > 28*24*time.Hour {
			return p, fmt.Errorf("timeout_max must be a duration from 0s to 672h")
		}
	}
	for _, v := range []string{p.Front.Answers.PerMember, p.Front.Answers.PerChannel} {
		if _, e := Budget(v); e != nil {
			return p, e
		}
	}
	return p, nil
}
