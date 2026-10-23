import test from 'node:test'
import assert from 'node:assert/strict'
import { activityLabel } from './activity.ts'

// A step label never shows an MCP tool ID or raw arguments.
test('a live step label drops the MCP server prefix and keeps the step', () => {
  assert.equal(activityLabel('mcp__dyna__shell: Read guide command'), 'Read guide command')
  assert.equal(activityLabel('mcp__dyna__shell: cd /home/ari/Work/resident-labs/dyna'), 'cd /home/ari/Work/resident-labs/dyna')
  assert.equal(activityLabel('mcp__other__get: {"query":"x"}'), 'get')
  assert.equal(activityLabel('mcp__pi__write: {"path":"a.go"}'), 'writing a file')
  assert.equal(activityLabel('mcp__dyna__shell: {"command":"make"}'), 'running a command')
  assert.equal(activityLabel('editing notify.go'), 'editing notify.go')
  assert.equal(activityLabel('Compacting context…'), 'Compacting context…')
  assert.equal(activityLabel('Thinking\nmore'), 'Thinking')
  assert.equal(activityLabel(undefined), '')
})
