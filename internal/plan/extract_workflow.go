package plan

import (
	"path/filepath"
	"strings"

	"dyna-agent/internal/textutil"
	"github.com/dop251/goja/ast"
)

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

func (ex *extractor) hookJob(call *ast.CallExpression, ctx walkCtx, sc *scope) {
	ex.visitCallArguments(call, ctx, sc)
	card := ctx.card
	if !ctx.inLoop {
		card = exactOne()
	}
	node := Node{
		Kind: KindJob, ParentID: ctx.parentID, GroupID: ctx.groupID,
		Label: "job", Phase: phaseOf(ctx), StageCallback: ctx.stageCallback,
		CallArguments: len(call.ArgumentList), CallTrailingComma: ex.callTrailingComma(call),
		Certainty: certOf(ctx), Cardinality: card,
	}
	ex.setCallMetadata(&node, call)
	id := ex.addNode(node)
	ex.edge(ctx.parentID, id, EdgeContains)
	if ctx.groupID != "" {
		kind := EdgeParallel
		if parentKind(ex, ctx.parentID) == KindPipeline {
			kind = EdgePipeline
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
