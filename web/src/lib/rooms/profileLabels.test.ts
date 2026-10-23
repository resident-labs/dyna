import test from 'node:test'
import assert from 'node:assert/strict'
import { modelLabel, profileHint } from './profileLabels.ts'

test('the "/" menu names a profile and tells two profiles on one model apart', () => {
  assert.equal(modelLabel('deepseek/deepseek-flash'), 'DeepSeek Flash')
  assert.equal(modelLabel('opencode-go/mimo-v2.6-pro'), 'MiMo v2.6 Pro')
  assert.equal(profileHint({ name: 'deepseek', harness: 'pi', model: 'deepseek/deepseek-flash', default: true }), 'Pi · DeepSeek Flash · default')
  const sol = profileHint({ name: 'sol', harness: 'codex', model: 'gpt-6.1-sol' })
  const fast = profileHint({ name: 'sol-ultrafast', harness: 'codex', model: 'gpt-6.1-sol' })
  assert.equal(sol, 'Codex · GPT 6.1 Sol')
  assert.equal(fast, 'Codex · GPT 6.1 Sol · Ultrafast')
})
