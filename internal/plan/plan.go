// Package plan extracts a conservative planned graph from workflow source
// without executing JavaScript, persists it as plan.json, instruments proven
// global hook calls, reconciles the plan with the live event stream, and
// projects a deterministic Mermaid view.
//
// All github.com/dop251/goja/parser usage is confined to this package.
package plan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"dyna-agent/internal/plan/ancestry"
	"dyna-agent/internal/store"
)

// ExtractorVersion is hashed into planHash. Bump when graph or instrumentation
// metadata extraction rules change.
const ExtractorVersion = "8"

const (
	StatusComplete    = "complete"
	StatusPartial     = "partial"
	StatusUnavailable = "unavailable"

	KindWorkflow = "workflow"
	KindPhase    = "phase"
	KindAgent    = "agent"
	KindJob      = "job"
	KindParallel = "parallel"
	KindPipeline = "pipeline"
	KindBranch   = "branch"
	KindLoop     = "loop"
	KindLog      = "log"
	KindUnknown  = "unknown"

	CertaintyCertain     = "certain"
	CertaintyConditional = "conditional"
	CertaintyTemplate    = "template"
	CertaintyUnknown     = "unknown"

	CardExact   = "exact"
	CardRange   = "range"
	CardUnknown = "unknown"

	EdgeSequence = "sequence"
	EdgeContains = "contains"
	EdgeBranch   = "branch"
	EdgeParallel = "parallel"
	EdgePipeline = "pipeline"
	EdgeLoop     = "loop"
	EdgeNested   = "nested"
	EdgeError    = "error"

	RootWorkflowID = "wf"

	PreviewLimit  = 80
	MaxGraphNodes = 80
	MaxGraphEdges = 120
)

// Options control extraction and nested script resolution.
type Options struct {
	// WriteAncestry is required when persisting nonempty resume ancestry.
	WriteAncestry AncestryWriter

	SourcePath  string
	WorkDir     string
	WorkflowDir string
	ResumedFrom string
	Now         time.Time
	// Depth is 0 for the invoking script. Literal child workflows are
	// extracted once at depth 1 and must not recurse further.
	Depth int
}

// Plan is the versioned planned-graph envelope persisted as plan.json.
type Plan struct {
	Version          int             `json:"version"`
	ExtractorVersion string          `json:"extractorVersion"`
	PlanHash         string          `json:"planHash"`
	SourcePath       string          `json:"sourcePath,omitempty"`
	SourceSHA256     string          `json:"sourceSha256"`
	Status           string          `json:"status"`
	Diagnostics      []Diagnostic    `json:"diagnostics,omitempty"`
	CreatedAt        time.Time       `json:"createdAt"`
	ResumedFrom      string          `json:"resumedFrom,omitempty"`
	Workflows        []Workflow      `json:"workflows,omitempty"`
	Phases           []PhaseDecl     `json:"phases,omitempty"`
	StageCallbacks   []StageCallback `json:"stageCallbacks,omitempty"`
	Nodes            []Node          `json:"nodes"`
	Edges            []Edge          `json:"edges"`
	Counts           Counts          `json:"counts"`
}

// Workflow describes one extracted workflow (root or depth-1 child).
type Workflow struct {
	ID         string `json:"id"`
	ParentID   string `json:"parentId,omitempty"`
	Label      string `json:"label,omitempty"`
	SourcePath string `json:"sourcePath,omitempty"`
	Ref        string `json:"ref,omitempty"`
	Depth      int    `json:"depth"`
}

// PhaseDecl is a declared meta.phases title.
type PhaseDecl struct {
	Title string `json:"title"`
}

// StageCallback describes an inline pipeline stage that can be wrapped in a
// per-invocation factory carrying immutable runtime context. Byte offsets refer
// to the callback's workflow script, not a wrapped parse buffer.
type StageCallback struct {
	ID                   string `json:"id"`
	PipelineNodeID       string `json:"pipelineNodeId"`
	GroupID              string `json:"groupId"`
	StageIndex           int    `json:"stageIndex"`
	WorkflowPlanID       string `json:"workflowPlanId"`
	CallbackStartByte    int    `json:"callbackStartByte"`
	CallbackEndByte      int    `json:"callbackEndByte"`
	ParameterOpeningByte int    `json:"parameterOpeningByte"`
	ParameterClosingByte int    `json:"parameterClosingByte"`
	ParameterCount       int    `json:"parameterCount"`
	Unparenthesized      bool   `json:"unparenthesized,omitempty"`
	TrailingComma        bool   `json:"trailingComma,omitempty"`
	HasRest              bool   `json:"hasRest,omitempty"`
	Name                 string `json:"name,omitempty"`
	NameStartByte        int    `json:"nameStartByte,omitempty"`
	NameEndByte          int    `json:"nameEndByte,omitempty"`
	RecursiveCalls       []Span `json:"recursiveCalls,omitempty"`
}

// Node is one planned graph node.
type Node struct {
	PlanNodeID        string      `json:"planNodeId"`
	WorkflowPlanID    string      `json:"workflowPlanId"`
	Kind              string      `json:"kind"`
	ParentID          string      `json:"parentId,omitempty"`
	GroupID           string      `json:"groupId,omitempty"`
	Label             string      `json:"label,omitempty"`
	Profile           string      `json:"profile,omitempty"`
	Phase             string      `json:"phase,omitempty"`
	PromptPreview     string      `json:"promptPreview,omitempty"`
	AuthorID          string      `json:"authorId,omitempty"`
	StageCallback     string      `json:"stageCallback,omitempty"`
	CallArguments     int         `json:"callArguments,omitempty"`
	CallTrailingComma bool        `json:"callTrailingComma,omitempty"`
	CalleeStartByte   int         `json:"calleeStartByte,omitempty"`
	CalleeEndByte     int         `json:"calleeEndByte,omitempty"`
	CallOpeningByte   int         `json:"callOpeningByte,omitempty"`
	CallClosingByte   int         `json:"callClosingByte,omitempty"`
	Source            Span        `json:"source"`
	Certainty         string      `json:"certainty"`
	Cardinality       Cardinality `json:"cardinality"`
}

// Span is a byte and line range into script.js (not a wrapped parse buffer).
type Span struct {
	StartByte int `json:"startByte"`
	EndByte   int `json:"endByte"`
	StartLine int `json:"startLine"`
	EndLine   int `json:"endLine"`
}

// Cardinality describes how many runtime instances a planned node may produce.
type Cardinality struct {
	Kind   string `json:"kind"`
	Min    *int   `json:"min,omitempty"`
	Max    *int   `json:"max,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// Edge connects two planned nodes.
type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
}

// Diagnostic is a non-fatal extract or persist note.
type Diagnostic struct {
	Level   string `json:"level"` // warning|error
	Code    string `json:"code"`
	Message string `json:"message"`
	NodeID  string `json:"nodeId,omitempty"`
}

// Counts summarizes graph size.
type Counts struct {
	Agents    int `json:"agents"`
	Phases    int `json:"phases"`
	Workflows int `json:"workflows"`
	Nodes     int `json:"nodes"`
	Edges     int `json:"edges"`
}

// SourceSHA256 returns the hex SHA-256 of the original workflow source.
func SourceSHA256(src string) string {
	sum := sha256.Sum256([]byte(src))
	return hex.EncodeToString(sum[:])
}

// ComputePlanHash covers extractorVersion + sourceSha256 + canonical node/edge identity.
func ComputePlanHash(extractorVersion, sourceSHA string, nodes []Node, edges []Edge) string {
	type identNode struct {
		ID, WF, Kind, Parent, Group, Author, Callback, Cert, Card string
		Start, End                                                int
	}
	type identEdge struct{ From, To, Kind string }
	inodes := make([]identNode, len(nodes))
	for i, n := range nodes {
		inodes[i] = identNode{
			ID: n.PlanNodeID, WF: n.WorkflowPlanID, Kind: n.Kind,
			Parent: n.ParentID, Group: n.GroupID, Author: n.AuthorID,
			Callback: n.StageCallback,
			Cert:     n.Certainty, Card: n.Cardinality.Kind,
			Start: n.Source.StartByte, End: n.Source.EndByte,
		}
	}
	iedges := make([]identEdge, len(edges))
	for i, e := range edges {
		iedges[i] = identEdge{From: e.From, To: e.To, Kind: e.Kind}
	}
	sort.Slice(inodes, func(i, j int) bool { return inodes[i].ID < inodes[j].ID })
	sort.Slice(iedges, func(i, j int) bool {
		if iedges[i].From != iedges[j].From {
			return iedges[i].From < iedges[j].From
		}
		if iedges[i].To != iedges[j].To {
			return iedges[i].To < iedges[j].To
		}
		return iedges[i].Kind < iedges[j].Kind
	})
	payload, _ := json.Marshal(struct {
		V   string      `json:"v"`
		SHA string      `json:"sha"`
		N   []identNode `json:"n"`
		E   []identEdge `json:"e"`
	}{V: extractorVersion, SHA: sourceSHA, N: inodes, E: iedges})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func finalize(p *Plan) {
	if p.Nodes == nil {
		p.Nodes = []Node{}
	}
	if p.Edges == nil {
		p.Edges = []Edge{}
	}
	p.PlanHash = ComputePlanHash(p.ExtractorVersion, p.SourceSHA256, p.Nodes, p.Edges)
	agents, phases, workflows := 0, 0, 0
	for _, n := range p.Nodes {
		switch n.Kind {
		case KindAgent:
			agents++
		case KindPhase:
			phases++
		case KindWorkflow:
			workflows++
		}
	}
	p.Counts = Counts{
		Agents: agents, Phases: phases, Workflows: workflows,
		Nodes: len(p.Nodes), Edges: len(p.Edges),
	}
}

// Unavailable returns a versioned envelope for a script that could not be parsed.
func Unavailable(src string, opts Options, reason string) *Plan {
	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	p := &Plan{
		Version:          1,
		ExtractorVersion: ExtractorVersion,
		SourcePath:       opts.SourcePath,
		SourceSHA256:     SourceSHA256(src),
		Status:           StatusUnavailable,
		Diagnostics:      []Diagnostic{{Level: "error", Code: "parse", Message: reason}},
		CreatedAt:        now,
		ResumedFrom:      opts.ResumedFrom,
		Nodes:            []Node{},
		Edges:            []Edge{},
	}
	finalize(p)
	return p
}

// MatchesSource reports whether p was extracted from src with this extractor.
func MatchesSource(p *Plan, src string) bool {
	if p == nil {
		return false
	}
	if p.ExtractorVersion != ExtractorVersion {
		return false
	}
	if p.SourceSHA256 != SourceSHA256(src) {
		return false
	}
	return p.PlanHash == ComputePlanHash(p.ExtractorVersion, p.SourceSHA256, p.Nodes, p.Edges)
}

// WriteAtomic writes an immutable generated graph blob. Resume ancestry goes
// through the engine into SQLite; stamping does not change the graph bytes.
func WriteAtomic(dir string, p *Plan, write AncestryWriter) error {
	if dir == "" {
		return preserveAncestryError(fmt.Errorf("plan directory is empty"), p.ResumedFrom)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return preserveAncestryError(err, p.ResumedFrom)
	}
	graph := *p
	graph.ResumedFrom = ""
	if err := writeGraph(dir, &graph); err != nil {
		return preserveAncestryError(err, p.ResumedFrom)
	}
	if p.ResumedFrom != "" {
		parent, err := stampAncestry(context.Background(), write, dir, p.ResumedFrom)
		if err != nil {
			return err
		}
		p.ResumedFrom = parent
	}
	return nil
}

// writeGraph stores the generated graph, which is retained when source changes.
func writeGraph(dir string, p *Plan) error {
	return store.WriteJSON(filepath.Join(dir, "plan.json"), p, 0o600)
}

// ReadFile loads the graph blob and takes resume ancestry only from SQLite.
func ReadFile(dir string) (*Plan, error) {
	p, err := readGraph(dir)
	if err != nil {
		return nil, err
	}
	p.ResumedFrom, err = (ancestry.Store{DataDir: store.DataDir()}).Parent(context.Background(), dir)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrAncestry, err)
	}
	return p, nil
}

// readGraph decodes the graph. Its resume header is only for the legacy importer.
func readGraph(dir string) (*Plan, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "plan.json"))
	if err != nil {
		return nil, err
	}
	var p Plan
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// stampResumeAncestry saves a parent only when the row has none. A later
// Ensure cannot replace an existing lineage. Graph identity stays unchanged.
func stampResumeAncestry(dir string, p *Plan, opts Options) error {
	if p == nil {
		return nil
	}
	want := strings.TrimSpace(opts.ResumedFrom)
	have := strings.TrimSpace(p.ResumedFrom)
	if want == "" || have != "" {
		return nil
	}
	parent, err := stampAncestry(context.Background(), opts.WriteAncestry, dir, want)
	if err != nil {
		return err
	}
	p.ResumedFrom = parent
	return nil
}

// Ensure writes a plan if one is missing. A matching existing plan is kept
// (including createdAt and graph identity). Resume ancestry is stamped onto
// that kept plan when opts.ResumedFrom is set and the row has none.
// A present but stale plan is returned with a diagnostic and is not overwritten.
func Ensure(dir, src string, opts Options) (*Plan, error) {
	return ensurePlan(dir, src, opts, func() *Plan { return Extract(src, opts) })
}

func ensurePlan(dir, src string, opts Options, create func() *Plan) (*Plan, error) {
	existing, err := ReadFile(dir)
	if err == nil && existing != nil {
		if err := stampResumeAncestry(dir, existing, opts); err != nil {
			return existing, err
		}
		if !MatchesSource(existing, src) {
			existing.Diagnostics = append(existing.Diagnostics, Diagnostic{
				Level: "warning", Code: "stale_source",
				Message: "existing plan.json does not match current source or extractor; left in place",
			})
		}
		return existing, nil
	}
	if err != nil && !os.IsNotExist(err) {
		return create(), preserveAncestryError(err, opts.ResumedFrom)
	}
	p := create()
	return p, WriteAtomic(dir, p, opts.WriteAncestry)
}

func preserveAncestryError(err error, parent string) error {
	if err != nil && strings.TrimSpace(parent) != "" && !errors.Is(err, ErrAncestry) {
		return fmt.Errorf("%w: %w", ErrAncestry, err)
	}
	return err
}

func intPtr(n int) *int { return &n }

// LookupNestedWorkflow finds the extracted child workflow id for a resolved
// script path or workflow() ref. Runtime nested-N ids are not plan ids.
func LookupNestedWorkflow(p *Plan, sourcePath string) (string, bool) {
	if p == nil || strings.TrimSpace(sourcePath) == "" {
		return "", false
	}
	want := filepath.Clean(sourcePath)
	var baseHits []string
	for _, w := range p.Workflows {
		if w.ID == "" || w.ID == RootWorkflowID {
			continue
		}
		if w.SourcePath != "" && filepath.Clean(w.SourcePath) == want {
			return w.ID, true
		}
		if w.Ref != "" && (w.Ref == sourcePath || filepath.Clean(w.Ref) == want) {
			return w.ID, true
		}
		if (w.SourcePath != "" && filepath.Base(w.SourcePath) == filepath.Base(want)) ||
			(w.Ref != "" && filepath.Base(w.Ref) == filepath.Base(want)) {
			baseHits = append(baseHits, w.ID)
		}
	}
	if len(baseHits) == 1 {
		return baseHits[0], true
	}
	return "", false
}

// ProjectWorkflow copies one child workflow's nodes and edges into a plan
// that shares the parent's remapped id space.
func ProjectWorkflow(parent *Plan, wfID, src string, opts Options) *Plan {
	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	sourcePath := opts.SourcePath
	if sourcePath == "" && parent != nil {
		for _, w := range parent.Workflows {
			if w.ID == wfID {
				sourcePath = w.SourcePath
				break
			}
		}
	}
	p := &Plan{
		Version:          1,
		ExtractorVersion: ExtractorVersion,
		SourcePath:       sourcePath,
		SourceSHA256:     SourceSHA256(src),
		Status:           StatusComplete,
		CreatedAt:        now,
		ResumedFrom:      opts.ResumedFrom,
	}
	if parent == nil || wfID == "" {
		finalize(p)
		return p
	}
	idSet := map[string]bool{}
	for _, n := range parent.Nodes {
		if n.WorkflowPlanID == wfID {
			p.Nodes = append(p.Nodes, n)
			idSet[n.PlanNodeID] = true
		}
	}
	for _, e := range parent.Edges {
		if idSet[e.From] && idSet[e.To] {
			p.Edges = append(p.Edges, e)
		}
	}
	for _, w := range parent.Workflows {
		if w.ID == wfID {
			p.Workflows = append(p.Workflows, w)
		}
	}
	for _, callback := range parent.StageCallbacks {
		if callback.WorkflowPlanID == wfID {
			p.StageCallbacks = append(p.StageCallbacks, callback)
		}
	}
	finalize(p)
	return p
}

// EnsureNested writes a child plan using the parent's inlined ids when the
// parent extracted that script. Falls back to an independent extract.
func EnsureNested(parentDir, childDir, src, sourcePath string, opts Options) (*Plan, error) {
	if opts.SourcePath == "" {
		opts.SourcePath = sourcePath
	}
	parent, err := ReadFile(parentDir)
	if errors.Is(err, ErrAncestry) {
		return Extract(src, opts), err
	}
	if err == nil && parent != nil {
		if wfID, ok := LookupNestedWorkflow(parent, sourcePath); ok {
			return ensurePlan(childDir, src, opts, func() *Plan { return ProjectWorkflow(parent, wfID, src, opts) })
		}
	}
	return Ensure(childDir, src, opts)
}
