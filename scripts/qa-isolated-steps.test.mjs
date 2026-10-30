import { test } from 'node:test';
import { strict as assert } from 'node:assert';
import { mkdtemp, mkdir, readFile, writeFile, symlink, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { execFileSync } from 'node:child_process';

test('sandbox output cannot redirect the host screenshot transfer', async () => {
  const dir = await mkdtemp(join(tmpdir(), 'qa-steps-'));
  try {
    const root = join(dir, 'root');
    await mkdir(root);
    const target = join(dir, 'result.png');
    const secret = join(dir, 'secret');
    const mapping = join(dir, 'trusted.json');
    const input = join(dir, 'input.json');
    await writeFile(secret, 'private');
    await writeFile(input, JSON.stringify([{ screenshot: target }]));
    const run = (...args) => execFileSync(process.execPath, ['scripts/qa-isolated-steps.mjs', ...args], { stdio: 'pipe' });
    run('prepare', root, mapping, input);
    await writeFile(join(root, 'screenshots.json'), JSON.stringify([{ source: secret, target: secret }]));
    await writeFile(join(root, 'screenshot-0.png'), 'image');
    run('finish', root, mapping);
    assert.equal(await readFile(target, 'utf8'), 'image');
    assert.equal(await readFile(secret, 'utf8'), 'private');

    await rm(join(root, 'screenshot-0.png'));
    await symlink(secret, join(root, 'screenshot-0.png'));
    assert.throws(() => run('finish', root, mapping));
    assert.equal(await readFile(target, 'utf8'), 'image');
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});
