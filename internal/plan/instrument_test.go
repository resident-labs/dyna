package plan

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestInstrumentInjectsPlanIDsOnProvenCalls(t *testing.T) {
	src := `export const meta = { name: 'i' }
phase('P')
log('hi')
await agent('do', { label: 'a' })
await parallel([() => agent('p', { label: 'p' })])
await pipeline(['x'], (i) => agent('s', { label: 's' }))
return 1
`
	p := Extract(src, Options{Now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
	out, err := Instrument(src, p)
	if err != nil {
		t.Fatal(err)
	}
	for _, helper := range []string{"__planAgent", "__planPhase", "__planLog", "__planParallel", "__planPipeline"} {
		if !strings.Contains(out, helper) {
			t.Fatalf("instrumented source missing %s:\n%s", helper, out)
		}
	}
	if strings.Count(out, "__planAgent") < 3 {
		t.Fatalf("expected 3 agent sites, got %s", out)
	}
}

func TestInstrumentSkipsShadowedAndUnavailable(t *testing.T) {
	src := `function go(agent) { return agent('no') }
return go(x => x)
`
	p := Extract(src, Options{Now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
	out, err := Instrument(src, p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "__planAgent") {
		t.Fatalf("shadowed call was instrumented:\n%s", out)
	}
	u := Unavailable("function (", Options{}, "bad")
	same, err := Instrument("function (", u)
	if err != nil || same != "function (" {
		t.Fatalf("unavailable instrument = %q %v", same, err)
	}
}

func TestInstrumentInjectsPipelineStageContextForms(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "parenthesized arrow",
			src:  `return pipeline([1], (value, original) => agent('work', {label: 'p'}))`,
			want: []string{`__planStage(__dynaCtx1 => (value, original) =>`, `, __dynaCtx1)`},
		},
		{
			name: "single unparenthesized arrow",
			src:  `return pipeline([1], value => agent('work', {label: 'p'}))`,
			want: []string{`__planStage(__dynaCtx1 => value =>`, `, __dynaCtx1)`},
		},
		{
			name: "zero parameter arrow",
			src:  `return pipeline([1], () => agent('work', {label: 'p'}))`,
			want: []string{`__planStage(__dynaCtx1 => () =>`, `, __dynaCtx1)`},
		},
		{
			name: "function expression",
			src:  `return pipeline([1], function (value) { return agent('work', {label: 'p'}) })`,
			want: []string{`__planStage(__dynaCtx1 => function (value)`, `, __dynaCtx1)`},
		},
		{
			name: "async parenthesized arrow",
			src:  `return pipeline([1], async (value) => agent('work', {label: 'p'}))`,
			want: []string{`__planStage(__dynaCtx1 => async (value) =>`, `, __dynaCtx1)`},
		},
		{
			name: "async unparenthesized arrow",
			src:  `return pipeline([1], async value => agent('work', {label: 'p'}))`,
			want: []string{`__planStage(__dynaCtx1 => async value =>`, `, __dynaCtx1)`},
		},
		{
			name: "async function expression",
			src:  `return pipeline([1], async function (value) { return agent('work', {label: 'p'}) })`,
			want: []string{`__planStage(__dynaCtx1 => async function (value)`, `, __dynaCtx1)`},
		},
		{
			name: "callback parameter trailing comma",
			src:  `return pipeline([1], (value,) => agent('work', {label: 'p'}))`,
			want: []string{`__planStage(__dynaCtx1 => (value,) =>`, `, __dynaCtx1)`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := Extract(tt.src, Options{})
			out, err := Instrument(tt.src, p)
			if err != nil {
				t.Fatalf("%v callbacks=%#v", err, p.StageCallbacks)
			}
			for _, want := range tt.want {
				if !strings.Contains(out, want) {
					t.Fatalf("instrumented source missing %q:\n%s", want, out)
				}
			}
		})
	}
}

func TestInstrumentPipelineStageContextFallbacks(t *testing.T) {
	t.Run("rest parameter injected", func(t *testing.T) {
		src := `return pipeline([1], (...values) => agent('work', {label: 'rest'}))`
		out, err := Instrument(src, Extract(src, Options{}))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "__planStage(__dynaCtx1 => (...values) => __planAgent") ||
			!strings.Contains(out, ", __dynaCtx1)") {
			t.Fatalf("rest callback was not context-injected:\n%s", out)
		}
	})

	t.Run("identifier callback untouched", func(t *testing.T) {
		src := `const stage = async value => agent('work', {label: 'named'}); return pipeline([1], stage)`
		out, err := Instrument(src, Extract(src, Options{}))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, "__dynaCtx") || strings.Contains(out, "__planStage") {
			t.Fatalf("identifier callback was context-injected:\n%s", out)
		}
		if !strings.Contains(out, "const stage = async value => __planAgent") {
			t.Fatalf("identifier callback agent was not otherwise instrumented:\n%s", out)
		}
	})
}

func TestInstrumentPipelineAgentTrailingComma(t *testing.T) {
	src := `return pipeline([1], value => agent('work', {label: 'comma'},))`
	out, err := Instrument(src, Extract(src, Options{}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `{label: 'comma'}, __dynaCtx1)`) || strings.Contains(out, ",, __dynaCtx1") {
		t.Fatalf("agent trailing comma was not handled:\n%s", out)
	}
}

func TestInstrumentPipelineAgentCommaMetadataIgnoresLiteralAndCommentContents(t *testing.T) {
	src := `return pipeline([1], async value => {
  await agent('RESPOND: ,//text', {label: 'string'})
  await agent(` + "`RESPOND: ${value},//template`" + `, {label: 'template'})
  await agent(/,\/\//, {label: 'regexp'})
  return agent('RESPOND: comment', {label: 'comment'} /* ,// still comment */,)
})`
	out, err := Instrument(src, Extract(src, Options{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`{label: 'string'}, __dynaCtx1)`,
		`{label: 'template'}, __dynaCtx1)`,
		`{label: 'regexp'}, __dynaCtx1)`,
		`/* ,// still comment */, __dynaCtx1)`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("instrumented source missing %q:\n%s", want, out)
		}
	}
	if parsed := Extract(out, Options{}); parsed.Status == StatusUnavailable {
		t.Fatalf("instrumented source is invalid: %#v\n%s", parsed.Diagnostics, out)
	}
}

func TestInstrumentPipelineStageContextInNestedStageExpressions(t *testing.T) {
	src := `const choose = true
return pipeline([1],
  ...[async value => { await sleep(1); return agent('spread', {label: 'spread'}) }],
  choose
    ? async value => { await sleep(1); return agent('conditional yes', {label: 'conditional-yes'}) }
    : async value => { await sleep(1); return agent('conditional no', {label: 'conditional-no'}) },
  (void 0, async value => { await sleep(1); return agent('sequence', {label: 'sequence'}) }),
)`
	p := Extract(src, Options{})
	out, err := Instrument(src, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.StageCallbacks) != 4 {
		t.Fatalf("stage callbacks = %#v\n%s", p.StageCallbacks, out)
	}
	wantStage := []int{0, 1, 1, 2}
	for i, want := range wantStage {
		if p.StageCallbacks[i].StageIndex != want {
			t.Fatalf("callback %d stage = %d, want %d: %#v", i, p.StageCallbacks[i].StageIndex, want, p.StageCallbacks)
		}
	}
	for _, want := range []string{
		`{label: 'spread'}, __dynaCtx1)`,
		`{label: 'conditional-yes'}, __dynaCtx2)`,
		`{label: 'conditional-no'}, __dynaCtx3)`,
		`{label: 'sequence'}, __dynaCtx4)`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("nested stage callback missing %q:\n%s", want, out)
		}
	}
}

func TestInstrumentPipelineStageContextInResolvedValueExpressions(t *testing.T) {
	src := `return pipeline([1],
  [async value => { await sleep(1); return agent('array', {label: 'array'}) }][0],
  ({callback: async value => { await sleep(1); return agent('object', {label: 'object'}) }}).callback,
  (() => async value => { await sleep(1); return agent('iife', {label: 'iife'}) })(),
)`
	p := Extract(src, Options{})
	out, err := Instrument(src, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.StageCallbacks) != 3 {
		t.Fatalf("stage callbacks = %#v\n%s", p.StageCallbacks, out)
	}
	for i, callback := range p.StageCallbacks {
		if callback.StageIndex != i {
			t.Fatalf("callback %d stage = %d, want %d: %#v", i, callback.StageIndex, i, p.StageCallbacks)
		}
		want := `{label: '` + []string{"array", "object", "iife"}[i] + `'}, __dynaCtx` + itoa(i+1) + `)`
		if !strings.Contains(out, want) {
			t.Fatalf("resolved stage expression missing %q:\n%s", want, out)
		}
	}
}

func TestInstrumentPipelineStageContextInReturnedFactoryValues(t *testing.T) {
	tests := []struct {
		name string
		src  string
	}{
		{
			name: "awaited async expression factory",
			src: `return pipeline([1], await (async () => async value => {
  await sleep(1)
  return agent('work', {label: 'async-factory'})
})())`,
		},
		{
			name: "simple block factory",
			src: `return pipeline([1], (() => {
  const harmless = 1
  return async value => {
    await sleep(harmless)
    return agent('work', {label: 'block-factory'})
  }
})())`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := Extract(tt.src, Options{})
			out, err := Instrument(tt.src, p)
			if err != nil {
				t.Fatal(err)
			}
			if len(p.StageCallbacks) != 1 || !strings.Contains(out, `, __dynaCtx1)`) {
				t.Fatalf("returned callback lacked lexical context: callbacks=%#v\n%s", p.StageCallbacks, out)
			}
		})
	}
}

func TestInstrumentNamedPipelineStageUsesStableSelfBinding(t *testing.T) {
	src := `return pipeline([1, 2], async function stage(value) { await sleep(1); stage.calls = (stage.calls || 0) + 1; return value })`
	out, err := Instrument(src, Extract(src, Options{}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `__planStage((__dynaCtx1, stage) => async function (value)`) ||
		!strings.Contains(out, `, "stage")`) {
		t.Fatalf("named callback self binding was not moved to stable wrapper:\n%s", out)
	}
}

func TestInstrumentNamedPipelineStageRecursiveCallsUseLexicalContext(t *testing.T) {
	src := `return pipeline([1, 2], async function stage(value) {
  await sleep(1)
  if (value > 0) return stage(value - 1)
  return agent('work', {label: 'recursive'})
})`
	p := Extract(src, Options{})
	out, err := Instrument(src, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.StageCallbacks) != 1 || len(p.StageCallbacks[0].RecursiveCalls) != 1 ||
		!strings.Contains(out, `(__dynaCtx1, stage, __dynaRecur1) => async function (value)`) ||
		!strings.Contains(out, `return __dynaRecur1(value - 1)`) {
		t.Fatalf("recursive self call was not context-bound: callbacks=%#v\n%s", p.StageCallbacks, out)
	}
}

func TestInstrumentNamedPipelineStageDoesNotRewriteShadowedSelfCalls(t *testing.T) {
	src := `return pipeline([1], async function stage(value) {
  if (value > 0) return stage(value - 1)
  {
    if (false) stage(value)
    const stage = value => value
    return stage(value)
  }
})`
	p := Extract(src, Options{})
	out, err := Instrument(src, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.StageCallbacks) != 1 || len(p.StageCallbacks[0].RecursiveCalls) != 1 ||
		!strings.Contains(out, `return __dynaRecur1(value - 1)`) ||
		!strings.Contains(out, `return stage(value)`) {
		t.Fatalf("self-call resolution was not lexical: callbacks=%#v\n%s", p.StageCallbacks, out)
	}
}

func TestInstrumentHelperBindingsAreHygienic(t *testing.T) {
	src := `const __planAgent = () => 5
const __planPipeline = () => [6]
const __planStage = () => 7
return pipeline([1], value => agent('work'))`
	out, err := Instrument(src, Extract(src, Options{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`async (__dynaPlanAgent1, __dynaPlanPipeline1, __dynaPlanStage1) =>`,
		`__dynaPlanPipeline1(`,
		`__dynaPlanStage1(`,
		`__dynaPlanAgent1(`,
		`globalThis.__planAgent, globalThis.__planPipeline, globalThis.__planStage`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("instrumented helper binding missing %q:\n%s", want, out)
		}
	}
}

func TestInstrumentPipelineStageContextNamesAreHygienic(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "parameter binding",
			src:  `return pipeline([1], (__dynaCtx1) => agent('work'))`,
			want: `__planStage(__dynaCtx2 => (__dynaCtx1) => __planAgent`,
		},
		{
			name: "local binding",
			src:  `return pipeline([1], value => { const __dynaCtx1 = value; return agent('work') })`,
			want: `__planStage(__dynaCtx2 => value => { const __dynaCtx1 = value;`,
		},
		{
			name: "captured outer binding",
			src:  `const __dynaCtx1 = 'outer'; return pipeline([1], value => agent(__dynaCtx1))`,
			want: `__planStage(__dynaCtx2 => value => __planAgent`,
		},
		{
			name: "escaped outer binding",
			src:  `const \u005f_dynaCtx1 = 'outer'; return pipeline([1], value => agent(\u005f_dynaCtx1))`,
			want: `__planStage(__dynaCtx2 => value => __planAgent`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := Instrument(tt.src, Extract(tt.src, Options{}))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, tt.want) || !strings.Contains(out, ", __dynaCtx2)") {
				t.Fatalf("context name collided with user source:\n%s", out)
			}
		})
	}
}

func TestInstrumentEscapedNamedPipelineStage(t *testing.T) {
	src := `return pipeline([1], async function \u0073tage(value) { await sleep(1); return agent(\u0073tage.name) })`
	out, err := Instrument(src, Extract(src, Options{}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `__planStage((__dynaCtx1, stage) => async function (value)`) ||
		!strings.Contains(out, `__planAgent`) || !strings.Contains(out, `, "stage")`) {
		t.Fatalf("escaped named callback was not safely instrumented:\n%s", out)
	}
}

func TestInstrumentUsesPersistedCallTokenOffsets(t *testing.T) {
	src := `return pipeline([1], async value => { await sleep(1); return \u0061gent /* legal separator */ ('work') })`
	extracted := Extract(src, Options{})
	encoded, err := json.Marshal(extracted)
	if err != nil {
		t.Fatal(err)
	}
	var persisted Plan
	if err := json.Unmarshal(encoded, &persisted); err != nil {
		t.Fatal(err)
	}
	out, err := Instrument(src, &persisted)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `__planAgent /* legal separator */ (`) ||
		!strings.Contains(out, `, __dynaCtx1)`) {
		t.Fatalf("persisted parser offsets were not used:\n%s", out)
	}
}

func TestInstrumentOptionalHookCallUsesParserOffsets(t *testing.T) {
	src := `return pipeline([1], async value => { await sleep(1); return agent?.('work') })`
	out, err := Instrument(src, Extract(src, Options{}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `__planAgent?.(`) || !strings.Contains(out, `, __dynaCtx1)`) {
		t.Fatalf("optional hook call was not instrumented with parser offsets:\n%s", out)
	}
	if parsed := Extract(out, Options{}); parsed.Status == StatusUnavailable {
		t.Fatalf("instrumented optional call is invalid: %#v\n%s", parsed.Diagnostics, out)
	}
}

func TestInstrumentNestedPipelineUsesInnermostStageContext(t *testing.T) {
	src := `return pipeline([1], async outer => {
  await pipeline([outer], inner => agent('inner', {label: 'inner'}))
  return agent('outer', {label: 'outer'})
})`
	p := Extract(src, Options{})
	out, err := Instrument(src, p)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`__planStage(__dynaCtx1 => async outer =>`,
		`__planStage(__dynaCtx2 => inner =>`,
		`{label: 'inner'}, __dynaCtx2)`,
		`{label: 'outer'}, __dynaCtx1)`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("nested pipeline missing %q:\n%s", want, out)
		}
	}
	if len(p.StageCallbacks) != 2 {
		t.Fatalf("stage callbacks = %#v", p.StageCallbacks)
	}
}

func TestInstrumentPipelineAgentsInParameterInitializers(t *testing.T) {
	tests := []struct {
		name string
		src  string
	}{
		{
			name: "arrow defaults and destructuring",
			src: `return pipeline([undefined], async (
  direct = agent('direct'),
  {deferred = async () => { await sleep(1); return agent('deferred') }} = {},
) => { await direct; return deferred() })`,
		},
		{
			name: "function expression default",
			src:  `return pipeline([undefined], function (value = agent('direct')) { return value })`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := Extract(tt.src, Options{})
			out, err := Instrument(tt.src, p)
			if err != nil {
				t.Fatal(err)
			}
			if len(p.StageCallbacks) != 1 || strings.Count(out, `__planAgent("`) == 0 ||
				strings.Count(out, "__dynaCtx1)") != strings.Count(out, `__planAgent("`) {
				t.Fatalf("parameter initializer agents lacked lexical context: callbacks=%#v\n%s", p.StageCallbacks, out)
			}
		})
	}
}

func TestInstrumentPipelineEmptyAgentCallsWithComments(t *testing.T) {
	for _, tt := range []struct {
		name string
		src  string
		want string
	}{
		{name: "block comment", src: `return pipeline([1], value => agent(/* block */))`, want: `/* block */undefined, undefined, __dynaCtx1)`},
		{name: "line comment", src: "return pipeline([1], value => agent(// line\n))", want: "// line\nundefined, undefined, __dynaCtx1)"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out, err := Instrument(tt.src, Extract(tt.src, Options{}))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, tt.want) {
				t.Fatalf("comment-only call was not treated as empty; want %q:\n%s", tt.want, out)
			}
		})
	}
}
