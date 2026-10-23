package plan

import (
	"regexp"
	"time"

	"dyna-agent/internal/workflowresolve"
	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/file"
	"github.com/dop251/goja/parser"
)

const asyncWrapPrefix = "(async () => {\n"

var exportMetaRe = regexp.MustCompile(`(?m)^(\s*)export(\s+const\s+meta\s*=)`)

var hookNames = map[string]bool{
	"agent": true, "workflow": true, "phase": true, "log": true,
	"parallel": true, "pipeline": true,
}

type extractor struct {
	orig            string
	opts            Options
	file            *file.File
	wrapPrefix      int
	nodes           []Node
	edges           []Edge
	diags           []Diagnostic
	workflows       []Workflow
	phases          []PhaseDecl
	stageCallbacks  []StageCallback
	seq             int
	groupSeq        int
	wfSeq           int
	callbackSeq     int
	authorSeen      map[string]string
	stageSelfScopes map[*scope]string
	status          string
	depth           int
	currentWF       string
}

type walkCtx struct {
	parentID      string
	groupID       string
	conditional   bool
	inLoop        bool
	phase         *string
	lastFlow      *string
	card          Cardinality
	stageCallback string
}

type scope struct {
	parent *scope
	bound  map[string]bool
}

func (s *scope) shadows(name string) bool {
	return s.resolve(name) != nil
}

func (s *scope) resolve(name string) *scope {
	for c := s; c != nil; c = c.parent {
		if c.bound[name] {
			return c
		}
	}
	return nil
}

func (s *scope) declare(name string) {
	if s == nil || name == "" {
		return
	}
	if s.bound == nil {
		s.bound = map[string]bool{}
	}
	s.bound[name] = true
}

func (s *scope) child() *scope {
	return &scope{parent: s, bound: map[string]bool{}}
}

// Extract builds a planned graph from workflow source without evaluating it.
func Extract(src string, opts Options) *Plan {
	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	p := &Plan{
		Version:          1,
		ExtractorVersion: ExtractorVersion,
		SourcePath:       opts.SourcePath,
		SourceSHA256:     SourceSHA256(src),
		CreatedAt:        now,
		ResumedFrom:      opts.ResumedFrom,
		Status:           StatusComplete,
	}
	ex := &extractor{
		orig: src, opts: opts, status: StatusComplete,
		authorSeen: map[string]string{}, stageSelfScopes: map[*scope]string{}, currentWF: RootWorkflowID,
	}
	prog, err := ex.parse(src)
	if err != nil {
		u := Unavailable(src, opts, err.Error())
		u.CreatedAt = now
		return u
	}
	metadata := metadataFromProgram(prog, ex.wrapPrefix > 0)
	ex.phases = metadata.Phases
	label := metadata.Name
	if label == "" {
		label = "workflow"
	}
	ex.workflows = []Workflow{{
		ID: RootWorkflowID, Label: label, SourcePath: opts.SourcePath, Depth: 0,
	}}
	root := ex.addNode(Node{
		PlanNodeID: RootWorkflowID, WorkflowPlanID: RootWorkflowID, Kind: KindWorkflow,
		Label: ex.workflows[0].Label, Certainty: CertaintyCertain,
		Cardinality: Cardinality{Kind: CardExact, Min: intPtr(1), Max: intPtr(1)},
	})
	last := ""
	phase := ""
	ctx := walkCtx{parentID: root, lastFlow: &last, phase: &phase, card: exactOne()}
	sc := &scope{bound: map[string]bool{}}
	ex.visitProgram(prog, ctx, sc)
	p.Status = ex.status
	p.Diagnostics = ex.diags
	p.Workflows = ex.workflows
	p.Phases = ex.phases
	p.StageCallbacks = ex.stageCallbacks
	p.Nodes = ex.nodes
	p.Edges = ex.edges
	if len(p.Diagnostics) > 0 && p.Status == StatusComplete {
		for _, d := range p.Diagnostics {
			if d.Level == "error" || d.Code == "nested_unresolved" || d.Code == "duplicate_author_id" {
				p.Status = StatusPartial
				break
			}
		}
	}
	finalize(p)
	return p
}

func (ex *extractor) parse(src string) (*ast.Program, error) {
	padded := padExportConstMeta(src)
	prog, err := parser.ParseFile(nil, "script.js", padded, 0, parser.WithDisableSourceMaps)
	if err == nil {
		ex.file = prog.File
		ex.wrapPrefix = 0
		return prog, nil
	}
	wrapped := asyncWrapPrefix + padded + "\n})()"
	prog, err2 := parser.ParseFile(nil, "script.js", wrapped, 0, parser.WithDisableSourceMaps)
	if err2 != nil {
		return nil, err
	}
	ex.file = prog.File
	ex.wrapPrefix = len(asyncWrapPrefix)
	return prog, nil
}

func padExportConstMeta(src string) string {
	return exportMetaRe.ReplaceAllString(src, "${1}      ${2}")
}

func (ex *extractor) nextID() string {
	ex.seq++
	return "n" + itoa(ex.seq)
}

func (ex *extractor) nextGroup() string {
	ex.groupSeq++
	return "g" + itoa(ex.groupSeq)
}

func (ex *extractor) nextWF() string {
	ex.wfSeq++
	return "wf" + itoa(ex.wfSeq)
}

func (ex *extractor) nextStageCallback() string {
	ex.callbackSeq++
	return "sc" + itoa(ex.callbackSeq)
}

func (ex *extractor) addNode(n Node) string {
	if n.PlanNodeID == "" {
		n.PlanNodeID = ex.nextID()
	}
	if n.WorkflowPlanID == "" {
		n.WorkflowPlanID = ex.currentWF
	}
	if n.Certainty == "" {
		n.Certainty = CertaintyCertain
	}
	if n.Cardinality.Kind == "" {
		n.Cardinality = exactOne()
	}
	ex.nodes = append(ex.nodes, n)
	return n.PlanNodeID
}

func (ex *extractor) edge(from, to, kind string) {
	if from == "" || to == "" || from == to {
		return
	}
	ex.edges = append(ex.edges, Edge{From: from, To: to, Kind: kind})
}

func (ex *extractor) diag(level, code, msg, nodeID string) {
	ex.diags = append(ex.diags, Diagnostic{Level: level, Code: code, Message: msg, NodeID: nodeID})
}

func (ex *extractor) spanOf(n ast.Node) Span {
	if n == nil || ex.file == nil {
		return Span{}
	}
	return ex.span(n.Idx0(), n.Idx1())
}

func (ex *extractor) span(a, b file.Idx) Span {
	start := ex.byteOff(a)
	end := ex.byteOff(b)
	if start < 0 {
		start = 0
	}
	if end < start {
		end = start
	}
	if end > len(ex.orig) {
		end = len(ex.orig)
	}
	sl, el := 1, 1
	if ex.file != nil {
		ps := ex.file.Position(int(a) - ex.file.Base())
		pe := ex.file.Position(int(b) - ex.file.Base())
		sl, el = ps.Line, pe.Line
		if ex.wrapPrefix > 0 {
			sl--
			el--
			if sl < 1 {
				sl = 1
			}
			if el < 1 {
				el = 1
			}
		}
	}
	return Span{StartByte: start, EndByte: end, StartLine: sl, EndLine: el}
}

func (ex *extractor) byteOff(idx file.Idx) int {
	if ex.file == nil {
		return 0
	}
	off := int(idx) - ex.file.Base()
	if ex.wrapPrefix > 0 {
		off -= ex.wrapPrefix
	}
	if off < 0 {
		return 0
	}
	return off
}

func (ex *extractor) flow(ctx walkCtx, id string) {
	if ctx.lastFlow == nil {
		return
	}
	if *ctx.lastFlow != "" {
		ex.edge(*ctx.lastFlow, id, EdgeSequence)
	}
	*ctx.lastFlow = id
}

func flowPrev(ctx walkCtx) string {
	if ctx.lastFlow == nil {
		return ""
	}
	return *ctx.lastFlow
}

func restoreFlow(ctx walkCtx, prev string) {
	if ctx.lastFlow != nil {
		*ctx.lastFlow = prev
	}
}

func (ex *extractor) dropIfNoChildren(id string, before int) bool {
	for _, n := range ex.nodes[before+1:] {
		if n.ParentID == id {
			return false
		}
	}
	// remove the empty structure node and its edges
	kept := ex.nodes[:0]
	for _, n := range ex.nodes {
		if n.PlanNodeID != id {
			kept = append(kept, n)
		}
	}
	ex.nodes = kept
	edges := ex.edges[:0]
	for _, e := range ex.edges {
		if e.From != id && e.To != id {
			edges = append(edges, e)
		}
	}
	ex.edges = edges
	return true
}

func parentKind(ex *extractor, id string) string {
	for _, n := range ex.nodes {
		if n.PlanNodeID == id {
			return n.Kind
		}
	}
	return ""
}

func phaseOf(ctx walkCtx) string {
	if ctx.phase == nil {
		return ""
	}
	return *ctx.phase
}

func certOf(ctx walkCtx) string {
	if ctx.inLoop {
		return CertaintyTemplate
	}
	if ctx.conditional {
		return CertaintyConditional
	}
	return CertaintyCertain
}

func exactOne() Cardinality {
	return Cardinality{Kind: CardExact, Min: intPtr(1), Max: intPtr(1)}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// ResolveScript locates a nested workflow using script-dir / workdir / registry / examples.
func ResolveScript(ref, scriptPath, workDir, workflowDir string) (string, string, error) {
	return workflowresolve.Resolve(ref, scriptPath, workDir, workflowDir)
}
