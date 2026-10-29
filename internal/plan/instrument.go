package plan

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/dop251/goja/ast"
)

type sourceEdit struct {
	start int
	end   int
	text  string
}

// Instrument rewrites proven global hook calls so runtime events carry
// planNodeId / planGroupId. Shadowed and non-root-workflow nodes are skipped.
// On failure the caller must keep the original source and today's regexp wrap.
func Instrument(src string, p *Plan) (string, error) {
	return InstrumentWorkflow(src, p, RootWorkflowID)
}

// InstrumentWorkflow rewrites proven calls that belong to wfID. The parent
// root uses RootWorkflowID; a nested child uses the parent's remapped
// workflow id so both sides share one id space.
func InstrumentWorkflow(src string, p *Plan, wfID string) (string, error) {
	if p == nil || p.Status == StatusUnavailable {
		return src, nil
	}
	if wfID == "" {
		wfID = RootWorkflowID
	}
	var edits []sourceEdit
	callbackCtx := make(map[string]string, len(p.StageCallbacks))
	ctxSeq := 0
	recurseSeq := 0
	identifierNames, err := decodedIdentifierNames(src)
	if err != nil {
		return "", fmt.Errorf("instrument: parse identifiers: %w", err)
	}
	helperAliases := makeInstrumentHelperAliases(p, wfID, identifierNames)
	for _, callback := range p.StageCallbacks {
		if !instrumentWorkflow(callback.WorkflowPlanID, wfID) {
			continue
		}
		ctxName := nextStageContextName(identifierNames, &ctxSeq)
		recurseName := ""
		if len(callback.RecursiveCalls) > 0 {
			recurseName = nextRecursiveStageName(identifierNames, &recurseSeq)
		}
		callbackEdits, err := instrumentStageCallback(src, callback, helperAliases["__planStage"], ctxName, recurseName)
		if err != nil {
			return "", err
		}
		edits = append(edits, callbackEdits...)
		callbackCtx[callback.ID] = ctxName
	}
	for _, n := range p.Nodes {
		if !instrumentWorkflow(n.WorkflowPlanID, wfID) {
			continue
		}
		runtimeHelper := instrumentHelper(n)
		if runtimeHelper == "" || n.Source.EndByte <= n.Source.StartByte {
			continue
		}
		helper := helperAliases[runtimeHelper]
		if n.Source.StartByte < 0 || n.Source.EndByte > len(src) {
			return "", fmt.Errorf("instrument: span out of range for %s", n.PlanNodeID)
		}
		paren := n.CallOpeningByte
		closing := n.CallClosingByte
		if paren < n.Source.StartByte || paren >= n.Source.EndByte || paren >= len(src) || src[paren] != '(' {
			return "", fmt.Errorf("instrument: no call paren for %s", n.PlanNodeID)
		}
		if closing <= paren || closing >= n.Source.EndByte || closing >= len(src) || src[closing] != ')' {
			return "", fmt.Errorf("instrument: no closing call paren for %s", n.PlanNodeID)
		}
		calleeStart, calleeEnd := n.CalleeStartByte, n.CalleeEndByte
		if calleeStart < n.Source.StartByte || calleeEnd <= calleeStart || calleeEnd > paren ||
			calleeEnd > len(src) || rawIdentifierEnd(src, calleeStart) != calleeEnd {
			return "", fmt.Errorf("instrument: invalid callee span for %s", n.PlanNodeID)
		}
		idArg := fmt.Sprintf("%q, ", instrumentID(n))
		if n.Kind == KindJob {
			idArg = fmt.Sprintf("%q, %t, ", instrumentID(n), n.FunctionScope)
		}
		edits = append(edits,
			sourceEdit{start: paren + 1, end: paren + 1, text: idArg},
			sourceEdit{start: calleeStart, end: calleeEnd, text: helper},
		)
		if n.Kind == KindAgent || n.Kind == KindJob {
			if ctxName := callbackCtx[n.StageCallback]; ctxName != "" {
				contextArg := ", " + ctxName
				if n.CallArguments == 0 {
					contextArg = "undefined, undefined, " + ctxName
				} else if n.CallTrailingComma {
					contextArg = " " + ctxName
				}
				edits = append(edits, sourceEdit{start: closing, end: closing, text: contextArg})
			}
		}
	}
	if len(edits) == 0 {
		return src, nil
	}
	sort.Slice(edits, func(i, j int) bool {
		if edits[i].start != edits[j].start {
			return edits[i].start > edits[j].start
		}
		return edits[i].end > edits[j].end
	})
	for i, edit := range edits {
		if edit.start < 0 || edit.end > len(src) || edit.start > edit.end {
			return "", fmt.Errorf("instrument: invalid edit")
		}
		if i > 0 && (edit.start == edits[i-1].start || edit.end > edits[i-1].start) {
			return "", fmt.Errorf("instrument: overlapping edits")
		}
	}
	out := src
	for _, e := range edits {
		out = out[:e.start] + e.text + out[e.end:]
	}
	return bindInstrumentHelpers(out, helperAliases), nil
}

func nextStageContextName(identifierNames map[string]bool, seq *int) string {
	for {
		(*seq)++
		name := fmt.Sprintf("__dynaCtx%d", *seq)
		if !identifierNames[name] {
			identifierNames[name] = true
			return name
		}
	}
}

func nextRecursiveStageName(identifierNames map[string]bool, seq *int) string {
	for {
		(*seq)++
		name := fmt.Sprintf("__dynaRecur%d", *seq)
		if !identifierNames[name] {
			identifierNames[name] = true
			return name
		}
	}
}

func makeInstrumentHelperAliases(p *Plan, wfID string, identifierNames map[string]bool) map[string]string {
	needed := map[string]bool{}
	for _, callback := range p.StageCallbacks {
		if instrumentWorkflow(callback.WorkflowPlanID, wfID) {
			needed["__planStage"] = true
		}
	}
	for _, node := range p.Nodes {
		if instrumentWorkflow(node.WorkflowPlanID, wfID) {
			if helper := instrumentHelper(node); helper != "" && node.Source.EndByte > node.Source.StartByte {
				needed[helper] = true
			}
		}
	}
	aliases := make(map[string]string, len(needed))
	for _, helper := range sortedInstrumentHelpers(needed) {
		alias := helper
		if identifierNames[alias] {
			base := "__dyna" + strings.ToUpper(helper[2:3]) + helper[3:]
			for seq := 1; ; seq++ {
				alias = fmt.Sprintf("%s%d", base, seq)
				if !identifierNames[alias] {
					break
				}
			}
		}
		identifierNames[alias] = true
		aliases[helper] = alias
	}
	return aliases
}

func sortedInstrumentHelpers(helpers map[string]bool) []string {
	names := make([]string, 0, len(helpers))
	for name := range helpers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func bindInstrumentHelpers(src string, aliases map[string]string) string {
	if len(aliases) == 0 {
		return src
	}
	helpers := sortedInstrumentHelpers(func() map[string]bool {
		needed := make(map[string]bool, len(aliases))
		for helper := range aliases {
			needed[helper] = true
		}
		return needed
	}())
	params := make([]string, 0, len(helpers))
	args := make([]string, 0, len(helpers))
	for _, helper := range helpers {
		params = append(params, aliases[helper])
		args = append(args, "globalThis."+helper)
	}
	// Evaluate the global helper references outside the nested function that
	// contains user code. Parser-derived aliases cannot be shadowed inside that
	// user lexical scope, including by var-hoisted or escaped identifiers.
	return "return await (async (" + strings.Join(params, ", ") + ") => {\n" + src +
		"\n})(" + strings.Join(args, ", ") + ");"
}

// decodedIdentifierNames derives hygiene information from the parsed AST, so
// escaped spellings collide with their JavaScript names just like plain text.
func decodedIdentifierNames(src string) (map[string]bool, error) {
	ex := extractor{orig: src}
	program, err := ex.parse(src)
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	collectDecodedIdentifiers(reflect.ValueOf(program), names, map[uintptr]bool{})
	return names, nil
}

func collectDecodedIdentifiers(value reflect.Value, names map[string]bool, seen map[uintptr]bool) {
	if !value.IsValid() {
		return
	}
	for value.Kind() == reflect.Interface {
		if value.IsNil() {
			return
		}
		value = value.Elem()
	}
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return
		}
		ptr := value.Pointer()
		if seen[ptr] {
			return
		}
		seen[ptr] = true
		value = value.Elem()
	}
	if value.Type() == reflect.TypeOf(ast.Identifier{}) {
		identifier := value.Interface().(ast.Identifier)
		names[identifier.Name.String()] = true
		return
	}
	switch value.Kind() {
	case reflect.Struct:
		if value.Type().PkgPath() != "github.com/dop251/goja/ast" {
			return
		}
		for i := 0; i < value.NumField(); i++ {
			collectDecodedIdentifiers(value.Field(i), names, seen)
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < value.Len(); i++ {
			collectDecodedIdentifiers(value.Index(i), names, seen)
		}
	}
}

func instrumentStageCallback(src string, callback StageCallback, stageHelper, ctxName, recurseName string) ([]sourceEdit, error) {
	if callback.CallbackStartByte < 0 || callback.CallbackEndByte < callback.CallbackStartByte ||
		callback.CallbackEndByte > len(src) {
		return nil, fmt.Errorf("instrument: callback span out of range for %s", callback.ID)
	}
	edits := []sourceEdit{
		{start: callback.CallbackStartByte, end: callback.CallbackStartByte, text: stageHelper + "(" + ctxName + " => "},
		{start: callback.CallbackEndByte, end: callback.CallbackEndByte, text: ")"},
	}
	if callback.Name != "" {
		if callback.NameStartByte < callback.CallbackStartByte || callback.NameEndByte <= callback.NameStartByte ||
			callback.NameEndByte > callback.CallbackEndByte || callback.NameEndByte > len(src) ||
			rawIdentifierEnd(src, callback.NameStartByte) != callback.NameEndByte {
			return nil, fmt.Errorf("instrument: callback name span out of range for %s", callback.ID)
		}
		factoryParams := ctxName + ", " + callback.Name
		if recurseName != "" {
			factoryParams += ", " + recurseName
		}
		edits[0].text = stageHelper + "((" + factoryParams + ") => "
		edits[1].text = ", " + fmt.Sprintf("%q", callback.Name) + ")"
		// Move the named expression's self binding to the stable public stage
		// wrapper while each invocation still closes over immutable context.
		edits = append(edits, sourceEdit{start: callback.NameStartByte, end: callback.NameEndByte})
		for _, call := range callback.RecursiveCalls {
			if recurseName == "" || call.StartByte < callback.CallbackStartByte || call.EndByte <= call.StartByte ||
				call.EndByte > callback.CallbackEndByte || call.EndByte > len(src) ||
				rawIdentifierEnd(src, call.StartByte) != call.EndByte {
				return nil, fmt.Errorf("instrument: recursive call span out of range for %s", callback.ID)
			}
			edits = append(edits, sourceEdit{start: call.StartByte, end: call.EndByte, text: recurseName})
		}
	}
	return edits, nil
}

func instrumentWorkflow(nodeWF, want string) bool {
	if want == RootWorkflowID || want == "" {
		return nodeWF == RootWorkflowID || nodeWF == ""
	}
	return nodeWF == want
}

func instrumentHelper(n Node) string {
	switch n.Kind {
	case KindAgent:
		return "__planAgent"
	case KindJob:
		return "__planJob"
	case KindWorkflow:
		if n.PlanNodeID == RootWorkflowID {
			return ""
		}
		return "__planWorkflow"
	case KindPhase:
		return "__planPhase"
	case KindLog:
		return "__planLog"
	case KindUnknown:
		if n.Label == "log" {
			return "__planLog"
		}
	case KindParallel:
		return "__planParallel"
	case KindPipeline:
		return "__planPipeline"
	}
	return ""
}

func instrumentID(n Node) string {
	if n.Kind == KindParallel || n.Kind == KindPipeline {
		if n.GroupID != "" {
			return n.GroupID
		}
	}
	return n.PlanNodeID
}
