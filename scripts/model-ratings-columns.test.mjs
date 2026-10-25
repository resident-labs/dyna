// Picks the model and baseline columns of an Artificial Analysis comparison
// table. Headers come from the real pages: the vendor prefix and the reasoning
// level must not hide a model, and the last cell is not a model.
import assert from 'node:assert/strict';
import test from 'node:test';
import { isColumn, num, pickColumns, slugOf, toSlug } from './model-ratings-columns.mjs';

test('pickColumns finds the baseline behind a vendor prefix', () => {
  const head = ['', 'Kimi Kimi K3.1 Pro', 'Anthropic Claude Opus 5.5 (Max, Default Fallback)', 'Add model'];
  const index = ['Intelligence Index', '55', '58', ''];
  assert.deepEqual(pickColumns(head, index, 'kimi-k3-1-pro', 'claude-opus-5-5'), { model: 1, baseline: 2 });
});

test('pickColumns takes the top reasoning level of each model', () => {
  const head = ['', 'OpenAI GPT 5.1 (low)', 'OpenAI GPT 5.1 (high)'];
  const index = ['Intelligence Index', '61', '68'];
  assert.deepEqual(pickColumns(head, index, 'gpt-5-1', 'claude-opus-5-5'), { model: 2, baseline: -1 });
});

test('a missing column reports -1 instead of a neighbouring model', () => {
  const head = ['', 'Kimi Kimi K3.1 Pro', 'Add model'];
  const index = ['Intelligence Index', '55', ''];
  assert.deepEqual(pickColumns(head, index, 'kimi-k3-1-pro', 'claude-opus-5-5'), { model: 1, baseline: -1 });
});

test('isColumn ignores the reasoning level and the vendor prefix', () => {
  assert.equal(isColumn('Anthropic Claude Opus 5.5 (Max, Default Fallback)', 'claude-opus-5-5'), true);
  assert.equal(isColumn('Kimi Kimi K3.1 Pro', 'kimi-k3-1-pro'), true);
  assert.equal(isColumn('Add model', 'claude-opus-5-5'), false);
  assert.equal(toSlug('Kimi Kimi K3.1 Pro'), 'kimi-kimi-k3-1-pro');
});

test('num reads priced and unit cells and rejects gaps', () => {
  assert.equal(num('$5.98'), 5.98);
  assert.equal(num('845.63s'), 845.63);
  assert.equal(num('70%'), 70);
  assert.equal(num(''), null);
  assert.equal(num(undefined), null);
});

test('slugOf reads a bare slug, a model page and a comparison page', () => {
  assert.equal(slugOf('kimi-k3-1-pro'), 'kimi-k3-1-pro');
  assert.equal(slugOf('https://artificialanalysis.ai/models/kimi-k3-1-pro'), 'kimi-k3-1-pro');
  assert.equal(
    slugOf('https://artificialanalysis.ai/models/comparisons/kimi-k3-1-pro-vs-claude-opus-5-5'),
    'kimi-k3-1-pro',
  );
  assert.equal(
    slugOf('https://artificialanalysis.ai/models/releases/comparisons/gpt-5-1-vs-claude-opus-5-5?tab=1'),
    'gpt-5-1',
  );
});