package plan

import (
	"dyna-agent/internal/workflowresolve"
	"dyna-agent/internal/textutil"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/file"
	"github.com/dop251/goja/parser"
	"github.com/dop251/goja/token"
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

func (ex *extractor) visitProgram(p *ast.Program, ctx walkCtx, sc *scope) {
	if p == nil {
		return
	}
	for _, d := range p.DeclarationList {
		ex.bindVarDecl(d, sc)
	}
	ex.visitStmts(p.Body, ctx, sc)
}

func (ex *extractor) visitStmts(list []ast.Statement, ctx walkCtx, sc *scope) {
	ex.prebindLexicalDeclarations(list, sc)
	for _, s := range list {
		ex.visitStmt(s, ctx, sc)
	}
}

func (ex *extractor) visitStmt(s ast.Statement, ctx walkCtx, sc *scope) {
	if s == nil {
		return
	}
	switch st := s.(type) {
	case *ast.BlockStatement:
		child := sc.child()
		ex.visitStmts(st.List, ctx, child)
	case *ast.ExpressionStatement:
		ex.visitExpr(st.Expression, ctx, sc)
	case *ast.ReturnStatement:
		ex.visitExpr(st.Argument, ctx, sc)
	case *ast.IfStatement:
		ex.visitIf(st, ctx, sc)
	case *ast.SwitchStatement:
		ex.visitSwitch(st, ctx, sc)
	case *ast.TryStatement:
		ex.visitTry(st, ctx, sc)
	case *ast.ForStatement:
		ex.visitLoop(st, st.Body, ctx, sc, "for")
	case *ast.ForInStatement:
		ex.visitLoop(st, st.Body, ctx, sc, "for-in")
	case *ast.ForOfStatement:
		ex.visitLoop(st, st.Body, ctx, sc, "for-of")
	case *ast.WhileStatement:
		ex.visitLoop(st, st.Body, ctx, sc, "while")
	case *ast.DoWhileStatement:
		ex.visitLoop(st, st.Body, ctx, sc, "do-while")
	case *ast.VariableStatement:
		for _, b := range st.List {
			ex.bindTarget(b.Target, sc)
			ex.visitExpr(b.Initializer, ctx, sc)
		}
	case *ast.LexicalDeclaration:
		for _, b := range st.List {
			ex.bindTarget(b.Target, sc)
			ex.visitExpr(b.Initializer, ctx, sc)
		}
	case *ast.FunctionDeclaration:
		if st.Function != nil && st.Function.Name != nil {
			sc.declare(st.Function.Name.Name.String())
		}
		ex.visitFunction(st.Function, ctx, sc)
	case *ast.ClassDeclaration:
		if st.Class != nil && st.Class.Name != nil {
			sc.declare(st.Class.Name.Name.String())
		}
		ex.visitClass(st.Class, ctx, sc)
	case *ast.LabelledStatement:
		ex.visitStmt(st.Statement, ctx, sc)
	case *ast.ThrowStatement:
		ex.visitExpr(st.Argument, ctx, sc)
	case *ast.WithStatement:
		ex.visitExpr(st.Object, ctx, sc)
		ex.visitStmt(st.Body, ctx, sc)
	}
}

func (ex *extractor) visitIf(st *ast.IfStatement, ctx walkCtx, sc *scope) {
	ex.visitExpr(st.Test, ctx, sc)
	prev := flowPrev(ctx)
	before := len(ex.nodes)
	id := ex.addNode(Node{
		Kind: KindBranch, ParentID: ctx.parentID, GroupID: ctx.groupID,
		Label: "if", Source: ex.spanOf(st), Certainty: certOf(ctx),
		Cardinality: exactOne(), Phase: phaseOf(ctx),
	})
	ex.edge(ctx.parentID, id, EdgeContains)
	ex.flow(ctx, id)
	arm := ctx
	arm.parentID = id
	arm.conditional = true
	arm.lastFlow = new(string)
	ex.visitStmt(st.Consequent, arm, sc.child())
	if st.Alternate != nil {
		alt := arm
		alt.lastFlow = new(string)
		ex.visitStmt(st.Alternate, alt, sc.child())
	}
	if ex.dropIfNoChildren(id, before) {
		restoreFlow(ctx, prev)
	}
}

func (ex *extractor) visitSwitch(st *ast.SwitchStatement, ctx walkCtx, sc *scope) {
	ex.visitExpr(st.Discriminant, ctx, sc)
	prev := flowPrev(ctx)
	before := len(ex.nodes)
	id := ex.addNode(Node{
		Kind: KindBranch, ParentID: ctx.parentID, GroupID: ctx.groupID,
		Label: "switch", Source: ex.spanOf(st), Certainty: certOf(ctx),
		Cardinality: exactOne(), Phase: phaseOf(ctx),
	})
	ex.edge(ctx.parentID, id, EdgeContains)
	ex.flow(ctx, id)
	for _, c := range st.Body {
		if c == nil {
			continue
		}
		ex.visitExpr(c.Test, ctx, sc)
		arm := ctx
		arm.parentID = id
		arm.conditional = true
		arm.lastFlow = new(string)
		ex.visitStmts(c.Consequent, arm, sc.child())
	}
	if ex.dropIfNoChildren(id, before) {
		restoreFlow(ctx, prev)
	}
}

func (ex *extractor) visitTry(st *ast.TryStatement, ctx walkCtx, sc *scope) {
	prev := flowPrev(ctx)
	before := len(ex.nodes)
	id := ex.addNode(Node{
		Kind: KindBranch, ParentID: ctx.parentID, GroupID: ctx.groupID,
		Label: "try", Source: ex.spanOf(st), Certainty: certOf(ctx),
		Cardinality: exactOne(), Phase: phaseOf(ctx),
	})
	ex.edge(ctx.parentID, id, EdgeContains)
	ex.flow(ctx, id)
	arm := ctx
	arm.parentID = id
	arm.conditional = true
	arm.lastFlow = new(string)
	if st.Body != nil {
		ex.visitStmts(st.Body.List, arm, sc.child())
	}
	if st.Catch != nil {
		cs := sc.child()
		if st.Catch.Parameter != nil {
			ex.bindTarget(st.Catch.Parameter, cs)
		}
		catch := arm
		catch.lastFlow = new(string)
		if st.Catch.Body != nil {
			ex.visitStmts(st.Catch.Body.List, catch, cs)
		}
	}
	if st.Finally != nil {
		fin := ctx
		fin.parentID = id
		fin.lastFlow = new(string)
		ex.visitStmts(st.Finally.List, fin, sc.child())
	}
	if ex.dropIfNoChildren(id, before) {
		restoreFlow(ctx, prev)
	}
}

func (ex *extractor) visitLoop(node ast.Node, body ast.Statement, ctx walkCtx, sc *scope, label string) {
	prev := flowPrev(ctx)
	before := len(ex.nodes)
	id := ex.addNode(Node{
		Kind: KindLoop, ParentID: ctx.parentID, GroupID: ctx.groupID,
		Label: label, Source: ex.spanOf(node), Certainty: CertaintyTemplate,
		Cardinality: Cardinality{Kind: CardUnknown, Reason: "loop"}, Phase: phaseOf(ctx),
	})
	ex.edge(ctx.parentID, id, EdgeContains)
	ex.flow(ctx, id)
	inner := ctx
	inner.parentID = id
	inner.inLoop = true
	inner.lastFlow = new(string)
	inner.card = Cardinality{Kind: CardUnknown, Reason: "loop"}
	ex.visitStmt(body, inner, sc.child())
	if ex.dropIfNoChildren(id, before) {
		restoreFlow(ctx, prev)
	}
}

func (ex *extractor) visitExpr(x ast.Expression, ctx walkCtx, sc *scope) {
	if x == nil {
		return
	}
	switch e := x.(type) {
	case *ast.CallExpression:
		ex.visitCall(e, ctx, sc)
	case *ast.AwaitExpression:
		ex.visitExpr(e.Argument, ctx, sc)
	case *ast.NewExpression:
		ex.visitExpr(e.Callee, ctx, sc)
		for _, a := range e.ArgumentList {
			ex.visitExpr(a, ctx, sc)
		}
	case *ast.ArrayLiteral:
		for _, v := range e.Value {
			ex.visitExpr(v, ctx, sc)
		}
	case *ast.ObjectLiteral:
		for _, p := range e.Value {
			ex.visitExpr(p, ctx, sc)
		}
	case *ast.PropertyKeyed:
		ex.visitExpr(e.Key, ctx, sc)
		ex.visitExpr(e.Value, ctx, sc)
	case *ast.PropertyShort:
		ex.visitExpr(e.Initializer, ctx, sc)
	case *ast.SpreadElement:
		ex.visitExpr(e.Expression, ctx, sc)
	case *ast.BinaryExpression:
		ex.visitExpr(e.Left, ctx, sc)
		ex.visitExpr(e.Right, ctx, sc)
	case *ast.AssignExpression:
		ex.visitExpr(e.Left, ctx, sc)
		ex.visitExpr(e.Right, ctx, sc)
	case *ast.UnaryExpression:
		ex.visitExpr(e.Operand, ctx, sc)
	case *ast.ConditionalExpression:
		ex.visitExpr(e.Test, ctx, sc)
		cond := ctx
		cond.conditional = true
		ex.visitExpr(e.Consequent, cond, sc)
		ex.visitExpr(e.Alternate, cond, sc)
	case *ast.SequenceExpression:
		for _, s := range e.Sequence {
			ex.visitExpr(s, ctx, sc)
		}
	case *ast.DotExpression:
		ex.visitExpr(e.Left, ctx, sc)
	case *ast.BracketExpression:
		ex.visitExpr(e.Left, ctx, sc)
		ex.visitExpr(e.Member, ctx, sc)
	case *ast.OptionalChain:
		ex.visitExpr(e.Expression, ctx, sc)
	case *ast.Optional:
		ex.visitExpr(e.Expression, ctx, sc)
	case *ast.FunctionLiteral:
		ex.visitFunction(e, ctx, sc)
	case *ast.ArrowFunctionLiteral:
		ex.visitArrow(e, ctx, sc)
	case *ast.ClassLiteral:
		ex.visitClass(e, ctx, sc)
	case *ast.TemplateLiteral:
		for _, sub := range e.Expressions {
			ex.visitExpr(sub, ctx, sc)
		}
	case *ast.YieldExpression:
		ex.visitExpr(e.Argument, ctx, sc)
	}
}

func (ex *extractor) visitCall(call *ast.CallExpression, ctx walkCtx, sc *scope) {
	ex.recordRecursiveStageCall(call, sc)
	if recv, cb, ok := mapLike(call); ok {
		ex.visitExpr(recv, ctx, sc)
		inner := ctx
		inner.inLoop = true
		if n, ok := closedArrayLen(recv); ok {
			inner.card = Cardinality{Kind: CardExact, Min: intPtr(n), Max: intPtr(n), Reason: "literal map"}
		} else {
			inner.card = Cardinality{Kind: CardUnknown, Reason: "dynamic list"}
		}
		ex.visitExpr(cb, inner, sc)
		for i, a := range call.ArgumentList {
			if i == 0 {
				continue
			}
			ex.visitExpr(a, ctx, sc)
		}
		return
	}
	name, ok := provenCallee(call, sc)
	if !ok {
		ex.visitExpr(call.Callee, ctx, sc)
		for _, a := range call.ArgumentList {
			ex.visitExpr(a, ctx, sc)
		}
		return
	}
	switch name {
	case "agent":
		ex.hookAgent(call, ctx, sc)
	case "workflow":
		ex.hookWorkflow(call, ctx, sc)
	case "phase":
		ex.hookPhase(call, ctx, sc)
	case "log":
		ex.hookLog(call, ctx, sc)
	case "parallel":
		ex.hookParallel(call, ctx, sc)
	case "pipeline":
		ex.hookPipeline(call, ctx, sc)
	}
}

func (ex *extractor) recordRecursiveStageCall(call *ast.CallExpression, sc *scope) {
	id := identCalleeNode(call.Callee)
	if id == nil {
		return
	}
	callbackID := ex.stageSelfScopes[sc.resolve(id.Name.String())]
	if callbackID == "" {
		return
	}
	start := ex.byteOff(id.Idx0())
	end := rawIdentifierEnd(ex.orig, start)
	for i := range ex.stageCallbacks {
		if ex.stageCallbacks[i].ID == callbackID {
			ex.stageCallbacks[i].RecursiveCalls = append(ex.stageCallbacks[i].RecursiveCalls, Span{
				StartByte: start,
				EndByte:   end,
			})
			return
		}
	}
}

func (ex *extractor) visitCallArguments(call *ast.CallExpression, ctx walkCtx, sc *scope) {
	for _, arg := range call.ArgumentList {
		ex.visitExpr(arg, ctx, sc)
	}
}

func (ex *extractor) callTrailingComma(call *ast.CallExpression) bool {
	if call == nil || len(call.ArgumentList) == 0 {
		return false
	}
	last := call.ArgumentList[len(call.ArgumentList)-1]
	start := ex.byteOff(last.Idx1())
	end := ex.byteOff(call.RightParenthesis)
	return trailingCommaAfterArgument(ex.orig, start, end)
}

func (ex *extractor) setCallMetadata(n *Node, call *ast.CallExpression) {
	if n == nil || call == nil {
		return
	}
	n.Source = ex.spanOf(call)
	n.CallOpeningByte = ex.byteOff(call.LeftParenthesis)
	n.CallClosingByte = ex.byteOff(call.RightParenthesis)
	if id := identCalleeNode(call.Callee); id != nil {
		n.CalleeStartByte = ex.byteOff(id.Idx0())
		n.CalleeEndByte = rawIdentifierEnd(ex.orig, n.CalleeStartByte)
	}
}

// trailingCommaAfterArgument examines only the parser-proven tail after the
// last argument. String, template, and regexp contents are part of that
// argument's AST span and therefore cannot be mistaken for comments here.
func trailingCommaAfterArgument(src string, start, end int) bool {
	if start < 0 || end < start || end > len(src) {
		return false
	}
	for i := start; i < end; {
		switch src[i] {
		case ' ', '\t', '\n', '\r':
			i++
		case ',':
			return true
		case '/':
			if i+1 >= end {
				return false
			}
			switch src[i+1] {
			case '/':
				i += 2
				for i < end && src[i] != '\n' && src[i] != '\r' {
					i++
				}
			case '*':
				i += 2
				for i+1 < end && (src[i] != '*' || src[i+1] != '/') {
					i++
				}
				if i+1 >= end {
					return false
				}
				i += 2
			default:
				return false
			}
		default:
			return false
		}
	}
	return false
}

func provenCallee(call *ast.CallExpression, sc *scope) (string, bool) {
	id := identCallee(call.Callee)
	if id == "" || !hookNames[id] {
		return "", false
	}
	if sc.shadows(id) {
		return "", false
	}
	return id, true
}

func identCallee(e ast.Expression) string {
	id := identCalleeNode(e)
	if id == nil {
		return ""
	}
	return id.Name.String()
}

func identCalleeNode(e ast.Expression) *ast.Identifier {
	for {
		switch x := e.(type) {
		case *ast.Identifier:
			return x
		case *ast.Optional:
			e = x.Expression
		case *ast.OptionalChain:
			e = x.Expression
		default:
			return nil
		}
	}
}

// rawIdentifierEnd starts from a parser-provided identifier offset and scans
// the corresponding source token. goja's Identifier.Idx1 uses the decoded
// name length, which is shorter than escaped spellings such as \u005f.
func rawIdentifierEnd(src string, start int) int {
	if start < 0 || start >= len(src) {
		return start
	}
	i := start
	first := true
	for i < len(src) {
		if src[i] == '\\' {
			next, ok := unicodeEscapeEnd(src, i)
			if !ok {
				break
			}
			i = next
			first = false
			continue
		}
		r, size := utf8.DecodeRuneInString(src[i:])
		if r == utf8.RuneError && size == 1 {
			break
		}
		valid := r == '_' || r == '$' || unicode.IsLetter(r) || unicode.Is(unicode.Nl, r) ||
			unicode.Is(unicode.Other_ID_Start, r) ||
			(!first && (unicode.IsDigit(r) || unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Mc, r) ||
				unicode.Is(unicode.Pc, r) || unicode.Is(unicode.Other_ID_Continue, r) || r == '\u200c' || r == '\u200d'))
		if !valid {
			break
		}
		i += size
		first = false
	}
	return i
}

func unicodeEscapeEnd(src string, start int) (int, bool) {
	if start+2 > len(src) || src[start] != '\\' || src[start+1] != 'u' {
		return start, false
	}
	i := start + 2
	if i < len(src) && src[i] == '{' {
		i++
		digits := 0
		for i < len(src) && src[i] != '}' {
			if !isHexByte(src[i]) {
				return start, false
			}
			i++
			digits++
		}
		if digits == 0 || i >= len(src) || src[i] != '}' {
			return start, false
		}
		return i + 1, true
	}
	if i+4 > len(src) {
		return start, false
	}
	for _, b := range []byte(src[i : i+4]) {
		if !isHexByte(b) {
			return start, false
		}
	}
	return i + 4, true
}

func isHexByte(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F'
}

func mapLike(call *ast.CallExpression) (recv, callback ast.Expression, ok bool) {
	var left ast.Expression
	var name string
	switch c := call.Callee.(type) {
	case *ast.DotExpression:
		left, name = c.Left, c.Identifier.Name.String()
	default:
		return nil, nil, false
	}
	switch name {
	case "map", "forEach", "flatMap", "filter":
	default:
		return nil, nil, false
	}
	if len(call.ArgumentList) == 0 {
		return nil, nil, false
	}
	return left, call.ArgumentList[0], true
}

func (ex *extractor) hookAgent(call *ast.CallExpression, ctx walkCtx, sc *scope) {
	prompt, promptOK := firstStaticString(call.ArgumentList)
	opts := objectArg(call, 1)
	label, _ := staticString(objectProp(opts, "label"))
	profile, _ := staticString(objectProp(opts, "profile"))
	phase, _ := staticString(objectProp(opts, "phase"))
	author, _ := staticString(objectProp(opts, "id"))
	if phase == "" {
		phase = phaseOf(ctx)
	}
	if label == "" && promptOK {
		label = textutil.ClipOneLine(prompt, PreviewLimit)
	}
	if label == "" {
		label = "agent"
	}
	cert := certOf(ctx)
	card := ctx.card
	if !ctx.inLoop {
		card = exactOne()
	}
	preview := ""
	if promptOK {
		preview = textutil.ClipOneLine(prompt, PreviewLimit)
	} else if len(call.ArgumentList) > 0 {
		preview = "${…}"
	}
	// JavaScript evaluates every argument before invoking the outer hook. Walk
	// them first so nested hooks retain lexical context and flow ordering.
	ex.visitCallArguments(call, ctx, sc)
	node := Node{
		Kind: KindAgent, ParentID: ctx.parentID, GroupID: ctx.groupID,
		Label: label, Profile: profile, Phase: phase, PromptPreview: preview,
		AuthorID: author, StageCallback: ctx.stageCallback,
		CallArguments: len(call.ArgumentList), CallTrailingComma: ex.callTrailingComma(call),
		Certainty: cert, Cardinality: card,
	}
	ex.setCallMetadata(&node, call)
	id := ex.addNode(node)
	if author != "" {
		if prev, ok := ex.authorSeen[author]; ok {
			ex.diag("warning", "duplicate_author_id", "duplicate agent opts.id "+author, id)
			_ = prev
		} else {
			ex.authorSeen[author] = id
		}
	}
	ex.edge(ctx.parentID, id, EdgeContains)
	if ctx.groupID != "" {
		kind := EdgeParallel
		if strings.HasPrefix(ctx.groupID, "g") {
			// distinguish via parent kind
			if parentKind(ex, ctx.parentID) == KindPipeline {
				kind = EdgePipeline
			}
		}
		ex.edge(ctx.groupID, id, kind)
	}
	ex.flow(ctx, id)
}

func (ex *extractor) hookPhase(call *ast.CallExpression, ctx walkCtx, sc *scope) {
	title, ok := firstStaticString(call.ArgumentList)
	if !ok {
		title = "phase"
	}
	ex.visitCallArguments(call, ctx, sc)
	node := Node{
		Kind: KindPhase, ParentID: ctx.parentID, GroupID: ctx.groupID,
		Label: title, Phase: title,
		Certainty: certOf(ctx), Cardinality: exactOne(),
	}
	ex.setCallMetadata(&node, call)
	id := ex.addNode(node)
	ex.edge(ctx.parentID, id, EdgeContains)
	ex.flow(ctx, id)
	if ok && ctx.phase != nil {
		*ctx.phase = title
	}
}

func (ex *extractor) hookLog(call *ast.CallExpression, ctx walkCtx, sc *scope) {
	preview, ok := firstStaticString(call.ArgumentList)
	if !ok {
		preview = "${…}"
	} else {
		preview = textutil.ClipOneLine(preview, PreviewLimit)
	}
	ex.visitCallArguments(call, ctx, sc)
	node := Node{
		Kind: KindLog, ParentID: ctx.parentID, GroupID: ctx.groupID,
		Label: "log", PromptPreview: preview, Phase: phaseOf(ctx),
		Certainty: certOf(ctx), Cardinality: exactOne(),
	}
	ex.setCallMetadata(&node, call)
	id := ex.addNode(node)
	ex.edge(ctx.parentID, id, EdgeContains)
	// log() is instrumented but is not a control-flow step.
}

func (ex *extractor) hookWorkflow(call *ast.CallExpression, ctx walkCtx, sc *scope) {
	ref, refOK := firstStaticString(call.ArgumentList)
	label := "workflow"
	if refOK {
		label = filepath.Base(strings.TrimSuffix(ref, filepath.Ext(ref)))
	}
	cert := certOf(ctx)
	if !refOK {
		cert = CertaintyUnknown
	}
	ex.visitCallArguments(call, ctx, sc)
	node := Node{
		Kind: KindWorkflow, ParentID: ctx.parentID, GroupID: ctx.groupID,
		Label: label, Phase: phaseOf(ctx),
		Certainty: cert, Cardinality: exactOne(),
	}
	ex.setCallMetadata(&node, call)
	id := ex.addNode(node)
	ex.edge(ctx.parentID, id, EdgeContains)
	ex.flow(ctx, id)
	if !refOK || ex.opts.Depth >= 1 {
		if !refOK {
			ex.diag("warning", "dynamic_workflow", "workflow reference is not a string literal", id)
		}
		return
	}
	path, childSrc, err := ResolveScript(ref, ex.opts.SourcePath, ex.opts.WorkDir, ex.opts.WorkflowDir)
	if err != nil {
		ex.diag("warning", "nested_unresolved", err.Error(), id)
		ex.status = StatusPartial
		return
	}
	childID := ex.nextWF()
	ex.workflows = append(ex.workflows, Workflow{
		ID: childID, ParentID: ex.currentWF, Label: label, SourcePath: path, Ref: ref, Depth: 1,
	})
	child := Extract(childSrc, Options{
		SourcePath: path, WorkDir: ex.opts.WorkDir, WorkflowDir: ex.opts.WorkflowDir,
		Now: ex.opts.Now, Depth: 1,
	})
	idMap := map[string]string{RootWorkflowID: id}
	for _, n := range child.Nodes {
		if n.PlanNodeID == RootWorkflowID {
			continue
		}
		idMap[n.PlanNodeID] = ex.nextID()
	}
	callbackMap := make(map[string]string, len(child.StageCallbacks))
	for _, callback := range child.StageCallbacks {
		mappedID := ex.nextStageCallback()
		callbackMap[callback.ID] = mappedID
		callback.ID = mappedID
		callback.WorkflowPlanID = childID
		if mapped, ok := idMap[callback.PipelineNodeID]; ok {
			callback.PipelineNodeID = mapped
		}
		ex.stageCallbacks = append(ex.stageCallbacks, callback)
	}
	for _, n := range child.Nodes {
		if n.PlanNodeID == RootWorkflowID {
			continue
		}
		n.PlanNodeID = idMap[n.PlanNodeID]
		n.WorkflowPlanID = childID
		if mapped, ok := idMap[n.ParentID]; ok {
			n.ParentID = mapped
		} else if n.ParentID == RootWorkflowID || n.ParentID == "" {
			n.ParentID = id
		}
		if mapped, ok := idMap[n.GroupID]; ok {
			n.GroupID = mapped
		}
		if mapped, ok := callbackMap[n.StageCallback]; ok {
			n.StageCallback = mapped
		}
		ex.nodes = append(ex.nodes, n)
	}
	for _, e := range child.Edges {
		from, to := e.From, e.To
		if mapped, ok := idMap[from]; ok {
			from = mapped
		} else if from == RootWorkflowID {
			from = id
		}
		if mapped, ok := idMap[to]; ok {
			to = mapped
		} else if to == RootWorkflowID {
			to = id
		}
		if from == to {
			continue
		}
		ex.edge(from, to, e.Kind)
	}
	ex.edge(id, idMapFirstChild(idMap, child), EdgeNested)
	_ = path
}

func idMapFirstChild(idMap map[string]string, child *Plan) string {
	for _, n := range child.Nodes {
		if n.PlanNodeID != RootWorkflowID && (n.ParentID == RootWorkflowID || n.ParentID == "") {
			if mapped, ok := idMap[n.PlanNodeID]; ok {
				return mapped
			}
		}
	}
	return ""
}

func (ex *extractor) hookParallel(call *ast.CallExpression, ctx walkCtx, sc *scope) {
	gid := ex.nextGroup()
	card := Cardinality{Kind: CardUnknown, Reason: "dynamic parallel list"}
	if len(call.ArgumentList) > 0 {
		if n, ok := closedArrayLen(call.ArgumentList[0]); ok {
			card = Cardinality{Kind: CardExact, Min: intPtr(n), Max: intPtr(n)}
		}
	}
	node := Node{
		Kind: KindParallel, ParentID: ctx.parentID, GroupID: gid,
		Label: "parallel", Certainty: certOf(ctx),
		Cardinality: card, Phase: phaseOf(ctx),
	}
	ex.setCallMetadata(&node, call)
	id := ex.addNode(node)
	ex.edge(ctx.parentID, id, EdgeContains)
	ex.flow(ctx, id)
	inner := ctx
	inner.parentID = id
	inner.groupID = gid
	inner.lastFlow = new(string)
	if len(call.ArgumentList) > 0 {
		ex.visitParallelArg(call.ArgumentList[0], inner, sc)
	}
	for i, a := range call.ArgumentList {
		if i == 0 {
			continue
		}
		ex.visitExpr(a, ctx, sc)
	}
}

func (ex *extractor) visitParallelArg(arg ast.Expression, ctx walkCtx, sc *scope) {
	if arr, ok := arg.(*ast.ArrayLiteral); ok && !arrayHasSpread(arr) {
		for _, el := range arr.Value {
			arm := ctx
			arm.lastFlow = new(string)
			ex.visitExpr(el, arm, sc)
		}
		return
	}
	ex.visitExpr(arg, ctx, sc)
}

func (ex *extractor) hookPipeline(call *ast.CallExpression, ctx walkCtx, sc *scope) {
	gid := ex.nextGroup()
	card := Cardinality{Kind: CardUnknown, Reason: "dynamic pipeline list"}
	if len(call.ArgumentList) > 0 {
		if n, ok := closedArrayLen(call.ArgumentList[0]); ok {
			card = Cardinality{Kind: CardExact, Min: intPtr(n), Max: intPtr(n)}
		}
	}
	node := Node{
		Kind: KindPipeline, ParentID: ctx.parentID, GroupID: gid,
		Label: "pipeline", Certainty: certOf(ctx),
		Cardinality: card, Phase: phaseOf(ctx),
	}
	ex.setCallMetadata(&node, call)
	id := ex.addNode(node)
	ex.edge(ctx.parentID, id, EdgeContains)
	ex.flow(ctx, id)
	if len(call.ArgumentList) > 0 {
		ex.visitExpr(call.ArgumentList[0], ctx, sc)
	}
	inner := ctx
	inner.parentID = id
	inner.groupID = gid
	inner.inLoop = true
	if card.Kind != CardExact {
		inner.card = Cardinality{Kind: CardUnknown, Reason: "pipeline items"}
	} else {
		inner.card = card
	}
	stageIndex := 0
	for _, arg := range call.ArgumentList[1:] {
		if spread, ok := arg.(*ast.SpreadElement); ok {
			if ex.visitPipelineSpread(spread.Expression, ctx, inner, sc, &stageIndex, id, gid) {
				continue
			}
			// The number of stages in a dynamic spread is unknowable statically.
			// Its expression still executes while the pipeline arguments are built.
			ex.visitExpr(spread.Expression, ctx, sc)
			stageIndex++
			continue
		}
		ex.visitPipelineStageExpr(arg, ctx, inner, sc, stageIndex, id, gid)
		stageIndex++
	}
}

// visitPipelineSpread expands literal stage arrays so every inline callback
// is associated with the stage position it has after JavaScript spread.
func (ex *extractor) visitPipelineSpread(expr ast.Expression, evalCtx, stageCtx walkCtx, sc *scope, stageIndex *int, pipelineID, groupID string) bool {
	arr, ok := expr.(*ast.ArrayLiteral)
	if !ok {
		return false
	}
	for _, element := range arr.Value {
		if nested, ok := element.(*ast.SpreadElement); ok {
			if ex.visitPipelineSpread(nested.Expression, evalCtx, stageCtx, sc, stageIndex, pipelineID, groupID) {
				continue
			}
			ex.visitExpr(nested.Expression, evalCtx, sc)
			*stageIndex++
			continue
		}
		ex.visitPipelineStageExpr(element, evalCtx, stageCtx, sc, *stageIndex, pipelineID, groupID)
		*stageIndex++
	}
	return true
}

// visitPipelineStageExpr follows expression forms whose value can be an
// inline callback. Non-candidate subexpressions are evaluated in the caller's
// context; only the function value invoked later by pipeline inherits the
// lexical stage context.
func (ex *extractor) visitPipelineStageExpr(expr ast.Expression, evalCtx, stageCtx walkCtx, sc *scope, stageIndex int, pipelineID, groupID string) {
	ex.visitPipelineStageValue(expr, evalCtx, stageCtx, sc, stageIndex, pipelineID, groupID, false)
}

func (ex *extractor) visitPipelineStageValue(expr ast.Expression, evalCtx, stageCtx walkCtx, sc *scope, stageIndex int, pipelineID, groupID string, awaited bool) {
	if expr == nil {
		return
	}
	switch candidate := expr.(type) {
	case *ast.ArrowFunctionLiteral, *ast.FunctionLiteral:
		callback, ok := ex.describeStageCallback(expr, pipelineID, groupID, stageIndex)
		if !ok {
			ex.visitExpr(expr, evalCtx, sc)
			return
		}
		ex.stageCallbacks = append(ex.stageCallbacks, callback)
		callbackCtx := stageCtx
		callbackCtx.lastFlow = new(string)
		callbackCtx.stageCallback = callback.ID
		ex.visitExpr(expr, callbackCtx, sc)
	case *ast.ConditionalExpression:
		ex.visitExpr(candidate.Test, evalCtx, sc)
		conditionalEval := evalCtx
		conditionalEval.conditional = true
		conditionalStage := stageCtx
		conditionalStage.conditional = true
		ex.visitPipelineStageValue(candidate.Consequent, conditionalEval, conditionalStage, sc, stageIndex, pipelineID, groupID, awaited)
		ex.visitPipelineStageValue(candidate.Alternate, conditionalEval, conditionalStage, sc, stageIndex, pipelineID, groupID, awaited)
	case *ast.SequenceExpression:
		if len(candidate.Sequence) == 0 {
			return
		}
		for _, prefix := range candidate.Sequence[:len(candidate.Sequence)-1] {
			ex.visitExpr(prefix, evalCtx, sc)
		}
		ex.visitPipelineStageValue(candidate.Sequence[len(candidate.Sequence)-1], evalCtx, stageCtx, sc, stageIndex, pipelineID, groupID, awaited)
	case *ast.AssignExpression:
		ex.visitExpr(candidate.Left, evalCtx, sc)
		ex.visitPipelineStageValue(candidate.Right, evalCtx, stageCtx, sc, stageIndex, pipelineID, groupID, awaited)
	case *ast.BinaryExpression:
		if candidate.Operator == token.LOGICAL_AND || candidate.Operator == token.LOGICAL_OR || candidate.Operator == token.COALESCE {
			conditionalEval := evalCtx
			conditionalEval.conditional = true
			conditionalStage := stageCtx
			conditionalStage.conditional = true
			ex.visitPipelineStageValue(candidate.Left, conditionalEval, conditionalStage, sc, stageIndex, pipelineID, groupID, awaited)
			ex.visitPipelineStageValue(candidate.Right, conditionalEval, conditionalStage, sc, stageIndex, pipelineID, groupID, awaited)
			return
		}
		ex.visitExpr(expr, evalCtx, sc)
	case *ast.AwaitExpression:
		ex.visitPipelineStageValue(candidate.Argument, evalCtx, stageCtx, sc, stageIndex, pipelineID, groupID, true)
	case *ast.OptionalChain:
		ex.visitPipelineStageValue(candidate.Expression, evalCtx, stageCtx, sc, stageIndex, pipelineID, groupID, awaited)
	case *ast.Optional:
		ex.visitPipelineStageValue(candidate.Expression, evalCtx, stageCtx, sc, stageIndex, pipelineID, groupID, awaited)
	case *ast.BracketExpression:
		if ex.visitPipelineBracketSelection(candidate, evalCtx, stageCtx, sc, stageIndex, pipelineID, groupID) {
			return
		}
		ex.visitExpr(expr, evalCtx, sc)
	case *ast.DotExpression:
		if ex.visitPipelineObjectSelection(candidate.Left, candidate.Identifier.Name.String(), nil,
			evalCtx, stageCtx, sc, stageIndex, pipelineID, groupID) {
			return
		}
		ex.visitExpr(expr, evalCtx, sc)
	case *ast.CallExpression:
		if ex.visitPipelineImmediateFactory(candidate, evalCtx, stageCtx, sc, stageIndex, pipelineID, groupID, awaited) {
			return
		}
		ex.visitExpr(expr, evalCtx, sc)
	default:
		ex.visitExpr(expr, evalCtx, sc)
	}
}

func (ex *extractor) visitPipelineBracketSelection(bracket *ast.BracketExpression, evalCtx, stageCtx walkCtx, sc *scope, stageIndex int, pipelineID, groupID string) bool {
	if bracket == nil {
		return false
	}
	key, ok := staticComputedPropertyKey(bracket.Member)
	if !ok {
		return false
	}
	switch value := bracket.Left.(type) {
	case *ast.ArrayLiteral:
		index, ok := arrayPropertyIndex(key)
		if !ok || index >= len(value.Value) || arrayHasSpread(value) {
			return false
		}
		for i, element := range value.Value {
			if i == index {
				ex.visitPipelineStageExpr(element, evalCtx, stageCtx, sc, stageIndex, pipelineID, groupID)
			} else {
				ex.visitExpr(element, evalCtx, sc)
			}
		}
		ex.visitExpr(bracket.Member, evalCtx, sc)
		return true
	case *ast.ObjectLiteral:
		return ex.visitPipelineObjectSelection(value, key, bracket.Member,
			evalCtx, stageCtx, sc, stageIndex, pipelineID, groupID)
	default:
		return false
	}
}

// visitPipelineObjectSelection follows a statically selected value property.
// Dynamic computed keys, spreads, and accessors can change which value is
// observed, so those objects deliberately retain best-effort global context.
func (ex *extractor) visitPipelineObjectSelection(expr ast.Expression, key string, member ast.Expression, evalCtx, stageCtx walkCtx, sc *scope, stageIndex int, pipelineID, groupID string) bool {
	obj, ok := expr.(*ast.ObjectLiteral)
	if !ok {
		return false
	}
	selected := -1
	for i, property := range obj.Value {
		switch property := property.(type) {
		case *ast.SpreadElement:
			return false
		case *ast.PropertyKeyed:
			propertyKey, known := staticPropertyKey(property.Key)
			if property.Computed {
				propertyKey, known = staticComputedPropertyKey(property.Key)
			}
			if !known {
				return false
			}
			if propertyKey == key {
				if property.Kind != ast.PropertyKindValue {
					selected = -2
					continue
				}
				selected = i
			}
		case *ast.PropertyShort:
			if property.Name.Name.String() == key {
				// A shorthand property resolves to an identifier rather than an
				// inline function value, so lexical wrapping is not possible.
				selected = -2
			}
		default:
			return false
		}
	}
	if selected < 0 {
		return false
	}
	for i, property := range obj.Value {
		switch property := property.(type) {
		case *ast.PropertyKeyed:
			if property.Computed {
				ex.visitExpr(property.Key, evalCtx, sc)
			}
			if i == selected {
				ex.visitPipelineStageExpr(property.Value, evalCtx, stageCtx, sc, stageIndex, pipelineID, groupID)
			} else {
				ex.visitExpr(property.Value, evalCtx, sc)
			}
		case *ast.PropertyShort:
			ex.visitExpr(property.Initializer, evalCtx, sc)
		}
	}
	if member != nil {
		ex.visitExpr(member, evalCtx, sc)
	}
	return true
}

func (ex *extractor) visitPipelineImmediateFactory(call *ast.CallExpression, evalCtx, stageCtx walkCtx, sc *scope, stageIndex int, pipelineID, groupID string, awaited bool) bool {
	factory, ok := describeImmediateFactory(call.Callee, awaited)
	if !ok {
		return false
	}
	// Arguments are evaluated in the caller before the inline factory is
	// entered. The returned expression is evaluated in the factory's scope.
	for _, argument := range call.ArgumentList {
		ex.visitExpr(argument, evalCtx, sc)
	}
	inner := sc.child()
	if factory.name != "" {
		inner.declare(factory.name)
	}
	for _, declaration := range factory.declarations {
		ex.bindVarDecl(declaration, inner)
	}
	if factory.params != nil {
		for _, binding := range factory.params.List {
			if binding != nil {
				ex.bindTarget(binding.Target, inner)
			}
		}
		if factory.params.Rest != nil {
			ex.bindTargetIdent(factory.params.Rest, inner)
		}
		ex.visitParameterInitializers(factory.params, evalCtx, inner)
	}
	for _, statement := range factory.prefix {
		ex.visitStmt(statement, evalCtx, inner)
	}
	ex.visitPipelineStageValue(factory.result, evalCtx, stageCtx, inner, stageIndex, pipelineID, groupID, false)
	return true
}

type immediateFactory struct {
	result       ast.Expression
	params       *ast.ParameterList
	declarations []*ast.VariableDeclaration
	prefix       []ast.Statement
	name         string
}

func describeImmediateFactory(expr ast.Expression, awaited bool) (immediateFactory, bool) {
	for {
		switch wrapped := expr.(type) {
		case *ast.Optional:
			expr = wrapped.Expression
		case *ast.OptionalChain:
			expr = wrapped.Expression
		default:
			goto unwrapped
		}
	}

unwrapped:
	switch fn := expr.(type) {
	case *ast.ArrowFunctionLiteral:
		if fn.Async && !awaited {
			return immediateFactory{}, false
		}
		switch body := fn.Body.(type) {
		case *ast.ExpressionBody:
			return immediateFactory{
				result: body.Expression, params: fn.ParameterList, declarations: fn.DeclarationList,
			}, true
		case *ast.BlockStatement:
			if result, prefix, ok := simpleFactoryBlock(body.List); ok {
				return immediateFactory{
					result: result, params: fn.ParameterList, declarations: fn.DeclarationList, prefix: prefix,
				}, true
			}
		}
	case *ast.FunctionLiteral:
		if (fn.Async && !awaited) || fn.Generator || fn.Body == nil {
			return immediateFactory{}, false
		}
		if result, prefix, ok := simpleFactoryBlock(fn.Body.List); ok {
			name := ""
			if fn.Name != nil {
				name = fn.Name.Name.String()
			}
			return immediateFactory{
				result: result, params: fn.ParameterList, declarations: fn.DeclarationList, prefix: prefix, name: name,
			}, true
		}
	}
	return immediateFactory{}, false
}

func simpleFactoryBlock(statements []ast.Statement) (ast.Expression, []ast.Statement, bool) {
	if len(statements) == 0 {
		return nil, nil, false
	}
	ret, ok := statements[len(statements)-1].(*ast.ReturnStatement)
	if !ok || ret.Argument == nil {
		return nil, nil, false
	}
	for _, statement := range statements[:len(statements)-1] {
		switch statement.(type) {
		case *ast.ExpressionStatement, *ast.VariableStatement, *ast.LexicalDeclaration,
			*ast.FunctionDeclaration, *ast.ClassDeclaration:
		default:
			return nil, nil, false
		}
	}
	return ret.Argument, statements[:len(statements)-1], true
}

func staticPropertyKey(expr ast.Expression) (string, bool) {
	switch value := expr.(type) {
	case *ast.Identifier:
		return value.Name.String(), true
	case *ast.StringLiteral:
		return value.Value.String(), true
	case *ast.NumberLiteral:
		return fmt.Sprint(value.Value), true
	case *ast.TemplateLiteral:
		return staticString(value)
	default:
		return "", false
	}
}

func staticComputedPropertyKey(expr ast.Expression) (string, bool) {
	switch expr.(type) {
	case *ast.Identifier:
		return "", false
	default:
		return staticPropertyKey(expr)
	}
}

func arrayPropertyIndex(key string) (int, bool) {
	if key == "" || strings.HasPrefix(key, "+") || strings.HasPrefix(key, "-") {
		return 0, false
	}
	parsed, err := strconv.ParseUint(key, 10, 31)
	if err != nil || strconv.FormatUint(parsed, 10) != key {
		return 0, false
	}
	return int(parsed), true
}

func (ex *extractor) describeStageCallback(expr ast.Expression, pipelineID, groupID string, stageIndex int) (StageCallback, bool) {
	var params *ast.ParameterList
	var callbackNode ast.Node
	var name string
	nameStart, nameEnd := 0, 0
	bodyStart := 0
	unparenthesized := false
	switch fn := expr.(type) {
	case *ast.ArrowFunctionLiteral:
		params = fn.ParameterList
		callbackNode = fn
		if params != nil {
			opening := ex.byteOff(params.Opening)
			bodyStart = ex.byteOff(fn.Body.Idx0())
			firstParam := bodyStart
			if len(params.List) > 0 && params.List[0] != nil {
				firstParam = ex.byteOff(params.List[0].Idx0())
			}
			callbackStart := ex.byteOff(fn.Idx0())
			prefix := ""
			if callbackStart >= 0 && callbackStart <= firstParam && firstParam <= len(ex.orig) {
				prefix = ex.orig[callbackStart:firstParam]
			}
			unparenthesized = (opening < 0 || opening >= len(ex.orig) || ex.orig[opening] != '(') && !strings.Contains(prefix, "(")
		}
	case *ast.FunctionLiteral:
		params = fn.ParameterList
		callbackNode = fn
		if fn.Name != nil {
			name = fn.Name.Name.String()
			nameStart = ex.byteOff(fn.Name.Idx0())
			nameEnd = rawIdentifierEnd(ex.orig, nameStart)
		}
	default:
		return StageCallback{}, false
	}
	if params == nil {
		return StageCallback{}, false
	}

	opening := ex.byteOff(params.Opening)
	closing := ex.byteOff(params.Closing)
	if unparenthesized && len(params.List) == 1 && params.List[0] != nil {
		opening = ex.byteOff(params.List[0].Idx0())
		closing = ex.byteOff(params.List[0].Idx1())
	} else if _, ok := expr.(*ast.ArrowFunctionLiteral); ok &&
		(opening < 0 || opening >= len(ex.orig) || ex.orig[opening] != '(') {
		callbackStart := ex.byteOff(callbackNode.Idx0())
		if rel := strings.Index(ex.orig[callbackStart:bodyStart], "("); rel >= 0 {
			opening = callbackStart + rel
		}
		if rel := strings.LastIndex(ex.orig[opening:bodyStart], ")"); rel >= 0 {
			closing = opening + rel
		}
	}
	trailingComma := false
	if !unparenthesized && len(params.List) > 0 {
		last := params.List[len(params.List)-1]
		if last != nil {
			end := ex.byteOff(last.Idx1())
			if end >= 0 && end <= closing && closing <= len(ex.orig) {
				trailingComma = trailingCommaAfterArgument(ex.orig, end, closing)
			}
		}
	}
	return StageCallback{
		ID: ex.nextStageCallback(), PipelineNodeID: pipelineID, GroupID: groupID,
		StageIndex: stageIndex, WorkflowPlanID: ex.currentWF,
		CallbackStartByte: ex.byteOff(callbackNode.Idx0()), CallbackEndByte: ex.byteOff(callbackNode.Idx1()),
		ParameterOpeningByte: opening, ParameterClosingByte: closing,
		ParameterCount: len(params.List), Unparenthesized: unparenthesized,
		TrailingComma: trailingComma, HasRest: params.Rest != nil,
		Name: name, NameStartByte: nameStart, NameEndByte: nameEnd,
	}, true
}

func (ex *extractor) visitFunction(fn *ast.FunctionLiteral, ctx walkCtx, sc *scope) {
	if fn == nil {
		return
	}
	nameScope := sc.child()
	inner := nameScope
	if fn.Name != nil {
		nameScope.declare(fn.Name.Name.String())
		if ex.isStageCallbackRoot(ctx.stageCallback, fn) {
			ex.stageSelfScopes[nameScope] = ctx.stageCallback
		}
		// Parameters, var declarations, and body lexical declarations can
		// shadow a named function expression's self binding.
		inner = nameScope.child()
	}
	for _, declaration := range fn.DeclarationList {
		ex.bindVarDecl(declaration, inner)
	}
	if fn.ParameterList != nil {
		for _, b := range fn.ParameterList.List {
			if b != nil {
				ex.bindTarget(b.Target, inner)
			}
		}
		if fn.ParameterList.Rest != nil {
			ex.bindTargetIdent(fn.ParameterList.Rest, inner)
		}
		ex.visitParameterInitializers(fn.ParameterList, ctx, inner)
	}
	if fn.Body != nil {
		ex.visitStmts(fn.Body.List, ctx, inner)
	}
}

func (ex *extractor) isStageCallbackRoot(callbackID string, fn *ast.FunctionLiteral) bool {
	if callbackID == "" || fn == nil {
		return false
	}
	start := ex.byteOff(fn.Idx0())
	for _, callback := range ex.stageCallbacks {
		if callback.ID == callbackID {
			return callback.CallbackStartByte == start
		}
	}
	return false
}

func (ex *extractor) visitArrow(fn *ast.ArrowFunctionLiteral, ctx walkCtx, sc *scope) {
	if fn == nil {
		return
	}
	inner := sc.child()
	for _, declaration := range fn.DeclarationList {
		ex.bindVarDecl(declaration, inner)
	}
	if fn.ParameterList != nil {
		for _, b := range fn.ParameterList.List {
			if b != nil {
				ex.bindTarget(b.Target, inner)
			}
		}
		if fn.ParameterList.Rest != nil {
			ex.bindTargetIdent(fn.ParameterList.Rest, inner)
		}
		ex.visitParameterInitializers(fn.ParameterList, ctx, inner)
	}
	switch b := fn.Body.(type) {
	case *ast.BlockStatement:
		ex.visitStmts(b.List, ctx, inner)
	case *ast.ExpressionBody:
		ex.visitExpr(b.Expression, ctx, inner)
	}
}

func (ex *extractor) prebindLexicalDeclarations(statements []ast.Statement, sc *scope) {
	for _, statement := range statements {
		switch declaration := statement.(type) {
		case *ast.LexicalDeclaration:
			for _, binding := range declaration.List {
				if binding != nil {
					ex.bindTarget(binding.Target, sc)
				}
			}
		case *ast.FunctionDeclaration:
			if declaration.Function != nil && declaration.Function.Name != nil {
				sc.declare(declaration.Function.Name.Name.String())
			}
		case *ast.ClassDeclaration:
			if declaration.Class != nil && declaration.Class.Name != nil {
				sc.declare(declaration.Class.Name.Name.String())
			}
		}
	}
}

// visitParameterInitializers walks expressions that execute while parameters
// are bound. They are not part of a function body in goja's AST, but they are
// lexically inside the function and therefore inherit a pipeline callback's
// stage context.
func (ex *extractor) visitParameterInitializers(params *ast.ParameterList, ctx walkCtx, sc *scope) {
	if params == nil {
		return
	}
	for _, binding := range params.List {
		if binding == nil {
			continue
		}
		ex.visitExpr(binding.Initializer, ctx, sc)
		ex.visitBindingInitializers(binding.Target, ctx, sc)
	}
}

func (ex *extractor) visitBindingInitializers(target ast.BindingTarget, ctx walkCtx, sc *scope) {
	switch pattern := target.(type) {
	case *ast.ArrayPattern:
		for _, element := range pattern.Elements {
			ex.visitBindingElementInitializers(element, ctx, sc)
		}
	case *ast.ObjectPattern:
		for _, property := range pattern.Properties {
			switch property := property.(type) {
			case *ast.PropertyShort:
				ex.visitExpr(property.Initializer, ctx, sc)
			case *ast.PropertyKeyed:
				if property.Computed {
					ex.visitExpr(property.Key, ctx, sc)
				}
				ex.visitBindingElementInitializers(property.Value, ctx, sc)
			}
		}
	}
}

func (ex *extractor) visitBindingElementInitializers(element ast.Expression, ctx walkCtx, sc *scope) {
	switch element := element.(type) {
	case *ast.AssignExpression:
		ex.visitExpr(element.Right, ctx, sc)
		if target, ok := element.Left.(ast.BindingTarget); ok {
			ex.visitBindingInitializers(target, ctx, sc)
		}
	case ast.BindingTarget:
		ex.visitBindingInitializers(element, ctx, sc)
	}
}

func (ex *extractor) visitClass(cl *ast.ClassLiteral, ctx walkCtx, sc *scope) {
	if cl == nil {
		return
	}
	ex.visitExpr(cl.SuperClass, ctx, sc)
	inner := sc.child()
	if cl.Name != nil {
		inner.declare(cl.Name.Name.String())
	}
	for _, el := range cl.Body {
		switch m := el.(type) {
		case *ast.MethodDefinition:
			ex.visitFunction(m.Body, ctx, inner)
		case *ast.FieldDefinition:
			ex.visitExpr(m.Initializer, ctx, inner)
		case *ast.ClassStaticBlock:
			if m.Block != nil {
				ex.visitStmts(m.Block.List, ctx, inner)
			}
		}
	}
}

func (ex *extractor) bindVarDecl(d *ast.VariableDeclaration, sc *scope) {
	if d == nil {
		return
	}
	for _, b := range d.List {
		ex.bindTarget(b.Target, sc)
	}
}

func (ex *extractor) bindTarget(t ast.BindingTarget, sc *scope) {
	if t == nil {
		return
	}
	switch b := t.(type) {
	case *ast.Identifier:
		sc.declare(b.Name.String())
	case *ast.ArrayPattern:
		for _, el := range b.Elements {
			ex.bindTargetIdent(el, sc)
		}
		ex.bindTargetIdent(b.Rest, sc)
	case *ast.ObjectPattern:
		for _, p := range b.Properties {
			switch pr := p.(type) {
			case *ast.PropertyShort:
				sc.declare(pr.Name.Name.String())
			case *ast.PropertyKeyed:
				ex.bindTargetIdent(pr.Value, sc)
			}
		}
		ex.bindTargetIdent(b.Rest, sc)
	}
}

func (ex *extractor) bindTargetIdent(e ast.Expression, sc *scope) {
	if e == nil {
		return
	}
	if assignment, ok := e.(*ast.AssignExpression); ok {
		ex.bindTargetIdent(assignment.Left, sc)
		return
	}
	if id, ok := e.(*ast.Identifier); ok {
		sc.declare(id.Name.String())
		return
	}
	if b, ok := e.(ast.BindingTarget); ok {
		ex.bindTarget(b, sc)
	}
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

func firstStaticString(args []ast.Expression) (string, bool) {
	if len(args) == 0 {
		return "", false
	}
	return staticString(args[0])
}

func staticString(e ast.Expression) (string, bool) {
	if e == nil {
		return "", false
	}
	switch x := e.(type) {
	case *ast.StringLiteral:
		return x.Value.String(), true
	case *ast.TemplateLiteral:
		if len(x.Expressions) == 0 && len(x.Elements) > 0 {
			var b strings.Builder
			for _, el := range x.Elements {
				b.WriteString(el.Parsed.String())
			}
			return b.String(), true
		}
	}
	return "", false
}

func objectArg(call *ast.CallExpression, i int) *ast.ObjectLiteral {
	if i >= len(call.ArgumentList) {
		return nil
	}
	o, _ := call.ArgumentList[i].(*ast.ObjectLiteral)
	return o
}

func objectProp(obj *ast.ObjectLiteral, name string) ast.Expression {
	if obj == nil {
		return nil
	}
	for _, p := range obj.Value {
		switch pr := p.(type) {
		case *ast.PropertyKeyed:
			if keyName(pr.Key) == name {
				return pr.Value
			}
		case *ast.PropertyShort:
			if pr.Name.Name.String() == name {
				if pr.Initializer != nil {
					return pr.Initializer
				}
				return &pr.Name
			}
		}
	}
	return nil
}

func keyName(e ast.Expression) string {
	switch x := e.(type) {
	case *ast.Identifier:
		return x.Name.String()
	case *ast.StringLiteral:
		return x.Value.String()
	}
	return ""
}

func closedArrayLen(e ast.Expression) (int, bool) {
	arr, ok := e.(*ast.ArrayLiteral)
	if !ok || arrayHasSpread(arr) {
		return 0, false
	}
	n := 0
	for _, v := range arr.Value {
		if v == nil {
			return 0, false
		}
		n++
	}
	return n, true
}

func arrayHasSpread(arr *ast.ArrayLiteral) bool {
	if arr == nil {
		return false
	}
	for _, v := range arr.Value {
		if _, ok := v.(*ast.SpreadElement); ok {
			return true
		}
	}
	return false
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
