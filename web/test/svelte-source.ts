import vm from 'node:vm'
import { transformSync } from 'esbuild'
import { parse } from 'svelte/compiler'
import ts from 'typescript'

// Tests run slices of component source in a vm context, and vm runs JavaScript only.
// esbuild strips the types of a lang="ts" slice; plain JavaScript passes through unchanged.
export function stripTypes(code: string): string {
  return transformSync(code, { loader: 'ts' }).code
}

// Svelte's parser adds source offsets that the estree types leave out.
export function sourceRange(node: object): { start: number, end: number } {
  if ('start' in node && typeof node.start === 'number' && 'end' in node && typeof node.end === 'number') return { start: node.start, end: node.end }
  throw new Error('the parsed node has no source range')
}

// A component's top-level functions by name, as JavaScript that vm can run.
// Offsets index the original text, so they stay correct when the script is lang="ts".
// names selects functions before type stripping, and the map stays in source order.
// Omit names to return every function.
export function topLevelFunctions(source: string, names?: readonly string[]): Map<string, string> {
  const found = new Map<string, string>()
  const wanted = names === undefined ? undefined : new Set(names)
  const body = parse(source, { modern: true }).instance?.content.body ?? []
  for (const node of body) {
    if (node.type !== 'FunctionDeclaration') continue
    if (wanted !== undefined && !wanted.has(node.id.name)) continue
    const { start, end } = sourceRange(node)
    found.set(node.id.name, stripTypes(source.slice(start, end)))
  }
  return found
}

// A plain TypeScript module's top-level functions by name, as JavaScript that
// vm can run. The screen's navigation moved out of App.svelte into such a
// module, so the same slicing works there, through the TypeScript parser.
export function moduleFunctions(source: string, names?: readonly string[]): Map<string, string> {
  const found = new Map<string, string>()
  const wanted = names === undefined ? undefined : new Set(names)
  const file = ts.createSourceFile('module.ts', source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
  for (const node of file.statements) {
    if (!ts.isFunctionDeclaration(node) || !node.name) continue
    if (wanted !== undefined && !wanted.has(node.name.text)) continue
    found.set(node.name.text, stripTypes(source.slice(node.getStart(file), node.getEnd())))
  }
  return found
}

// Runs the named functions of a component in the context, in the given order.
export function runFunctions(source: string, names: readonly string[], context: Record<string, unknown>): void {
  const functions = topLevelFunctions(source, names)
  const code = names.map((name) => {
    const text = functions.get(name)
    if (text === undefined) throw new Error(`no top-level function ${name} in the component`)
    return text
  })
  vm.runInContext(code.join('\n'), vm.createContext(context))
}

// Calls a function that runFunctions defined in the context.
export function call(context: Record<string, unknown>, name: string, ...args: unknown[]): unknown {
  const fn: unknown = context[name]
  if (typeof fn !== 'function') throw new Error(`${name} is not a function in the context`)
  return fn(...args)
}
