package plan

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/token"
)

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
