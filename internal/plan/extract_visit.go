package plan

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/dop251/goja/ast"
)

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
