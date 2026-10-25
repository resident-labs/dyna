import test from 'node:test'
import assert from 'node:assert/strict'
import { nativeHarness, openCodeKey, runningFlow, harnessPin, sharedLogin } from './signin.ts'
import type { HarnessSnapshot, PrivateLogin } from '../../api/harness.ts'

test('a saved sign-in is recovered for the account that asked for it', () => {
  const native: PrivateLogin = { accountId: 'native-account', harness: 'claude-code' }
  const piAgain: PrivateLogin = { accountId: 'pi:provider', harness: 'pi', target: 'pi-account' }
  const genericPi: PrivateLogin = { accountId: 'pi:provider', harness: 'pi' }
  assert.equal(runningFlow([native, piAgain], { id: 'native-account' }), native, 'a native card names its account')
  assert.equal(runningFlow([native, piAgain], { id: 'pi-account' }), piAgain, 'a Pi card names its target')
  assert.equal(runningFlow([native, piAgain], { id: 'other' }), null, 'another account keeps its form')
  assert.equal(runningFlow([piAgain, genericPi], null), genericPi, 'a new account takes the saved Pi card only')
  assert.equal(runningFlow([native], null), native, 'a new native card is recoverable too')
  assert.equal(runningFlow([], null), null)
})

test('a provider names its native harness and its OpenCode key', () => {
  assert.equal(nativeHarness('anthropic'), 'claude-code')
  assert.equal(nativeHarness('chatgpt'), 'codex')
  assert.equal(nativeHarness('opencode-go'), 'opencode')
  assert.equal(nativeHarness('meta-muse'), 'muse')
  assert.equal(nativeHarness('deepseek'), '', 'Pi alone signs in to DeepSeek')
  assert.equal(openCodeKey('chatgpt'), 'openai')
  assert.equal(openCodeKey('meta-muse'), '', 'OpenCode takes no Muse key')
})

test('a sign-in uses the installed or recommended copy of its harness', () => {
  const snapshot: HarnessSnapshot = {
    bundles: [{ harness: 'codex', version: '0.163.0', executable: '', directory: '' }],
    accounts: [],
    profiles: [],
    installing: [],
    selectedVersions: {},
    recommendedVersions: [{ harness: 'codex', version: '0.164.0', status: 'resolved', source: 'npm' }],
    capabilities: [{ harness: 'muse', status: 'ready', reason: '', installSupported: true, accountBlocked: '', mcp: true, skills: true, skillsPinned: false, concurrent: false, sharedPersonalLogin: true }],
  }
  assert.equal(harnessPin(snapshot, 'codex'), '0.163.0', 'an installed copy wins')
  assert.equal(harnessPin({ ...snapshot, bundles: [] }, 'codex'), '0.164.0', 'the recommendation is the next choice')
  assert.equal(harnessPin(null, 'codex'), '')
  assert.equal(sharedLogin(snapshot, 'muse'), true)
  assert.equal(sharedLogin(snapshot, 'codex'), false)
})
