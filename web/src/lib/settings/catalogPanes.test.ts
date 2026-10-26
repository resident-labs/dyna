import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { errorText } from '../errorText.ts'
import { call, runFunctions } from '../../../test/svelte-source.ts'
import { need } from '../../../test/need.ts'
import type { HarnessConfig, ManagedMCP, ManagedSkill } from '../api/harness.ts'

const read = (path: string): string => readFileSync(new URL(path, import.meta.url), 'utf8')
const skillsPane = read('./skills/SkillsPane.svelte')
const skillForm = read('./skills/SkillForm.svelte')
const mcpPane = read('./mcp/McpPane.svelte')
const mcpForm = read('./mcp/McpForm.svelte')

const catalog = (mcp: ManagedMCP[] = [], skills: ManagedSkill[] = []): HarnessConfig => ({ mcp, skills })
const helpers = { errorText, failText: (e: unknown, fallback: string) => (e ? errorText(e) : fallback) }

test('a skill is removed by name alone', async () => {
  const calls: unknown[][] = []
  const context: { removing: string; catalog: HarnessConfig | null; [name: string]: unknown } = {
    ...helpers, busy: false, stopped: false, error: '', notice: '', removing: 'release-notes', catalog: catalog([], [{ name: 'release-notes', source: '/skills/release-notes', enabled: true }]),
    api: { removeHarnessConfig: async (...args: unknown[]) => { calls.push(args); return catalog() } },
  }
  runFunctions(skillsPane, ['removeSkill'], context)
  await call(context, 'removeSkill')
  assert.deepEqual(calls, [['skill', 'release-notes']])
  assert.equal(context.removing, '')
})

test('a failed skill add shows the server error', async () => {
  const context: { name: string; source: string; error: string; [name: string]: unknown } = {
    errorText, busy: false, error: '', name: 'review', source: 'relative/dir',
    api: { addHarnessSkill: async () => { throw new Error('the source must be an absolute directory') } },
    onsave: () => { throw new Error('a failed add must not refresh the catalog') },
  }
  runFunctions(skillForm, ['save'], context)
  await call(context, 'save')
  assert.match(context.error, /absolute directory/)
})

test('an MCP switch sends the harness it belongs to', async () => {
  const calls: unknown[][] = []
  const context: { catalog: HarnessConfig | null; [name: string]: unknown } = {
    ...helpers, busy: false, stopped: false, error: '', notice: '', catalog: catalog([{ name: 'github', command: 'gh-mcp', enabledHarnesses: [] }]),
    api: { setHarnessConfigEnabled: async (...args: unknown[]) => { calls.push(args); return catalog() } },
  }
  runFunctions(mcpPane, ['setEnabled'], context)
  await call(context, 'setEnabled', { name: 'github', command: 'gh-mcp', enabledHarnesses: [] }, 'pi', true)
  assert.deepEqual(calls, [['mcp', 'github', 'pi', true]])
})

test('removing an MCP server drops the whole catalog entry', async () => {
  const calls: unknown[][] = []
  const context: { removing: string; catalog: HarnessConfig | null; [name: string]: unknown } = {
    ...helpers, busy: false, stopped: false, error: '', notice: '', removing: 'github', catalog: catalog([{ name: 'github', command: 'gh-mcp', enabledHarnesses: ['pi'] }]),
    api: { removeHarnessConfig: async (...args: unknown[]) => { calls.push(args); return catalog() } },
  }
  runFunctions(mcpPane, ['removeServer'], context)
  await call(context, 'removeServer')
  assert.deepEqual(calls, [['mcp', 'github']])
  assert.equal(context.removing, '')
})

test('adding an MCP server sends a URL or a typed command line', async () => {
  const sent: ManagedMCP[] = []
  const context: { mode: string; url: string; [name: string]: unknown } = {
    errorText, busy: false, error: '', name: 'browser', mode: 'url', url: ' https://example.com/mcp ',
    api: { addHarnessMCP: async (entry: ManagedMCP) => { sent.push(entry); return catalog() } },
    onsave: () => {},
  }
  runFunctions(mcpForm, ['argumentList', 'save'], context)
  await call(context, 'save')
  assert.equal(JSON.stringify(sent), JSON.stringify([{ name: 'browser', enabledHarnesses: [], url: 'https://example.com/mcp' }]))
})
