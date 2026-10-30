// Move only the requested steps and screenshots across the sandbox boundary.
import { readFile, writeFile, open } from 'node:fs/promises';
import { constants } from 'node:fs';
import { resolve, join } from 'node:path';

const [action, root, mappingFile, input] = process.argv.slice(2);
if (action === 'prepare') {
  const steps = JSON.parse(await readFile(input, 'utf8'));
  if (!Array.isArray(steps) || !steps.length) throw new Error('QA steps must be a nonempty list');
  const outputs = [];
  for (const step of steps) {
    if (typeof step.screenshot === 'string') {
      const target = resolve(step.screenshot);
      const source = join(root, 'screenshot-' + outputs.length + '.png');
      outputs.push({ source, target });
      step.screenshot = source;
    }
  }
  await writeFile(mappingFile, JSON.stringify(outputs));
  await writeFile(join(root, 'steps.json'), JSON.stringify(steps));
} else if (action === 'finish') {
  const outputs = JSON.parse(await readFile(mappingFile, 'utf8'));
  for (const { source, target } of outputs) {
    // The sandbox can replace its output with a symlink. Never follow it on the host.
    const file = await open(source, constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK);
    try {
      if (!(await file.stat()).isFile()) throw new Error('QA screenshot is not a regular file');
      await writeFile(target, await file.readFile());
    } finally {
      await file.close();
    }
    console.log('QA page screenshot: ' + target);
  }
} else {
  throw new Error('unknown QA steps action');
}
