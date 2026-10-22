// A node test reporter that writes one JSON line per finished test: its title and
// the test file it ran in. Node 22's JUnit reporter has no file attribute, so the
// CI checker reads this to re-run a slow test alone.
import { relative } from 'node:path'

interface TestEvent {
  type: string
  data: { name?: unknown; file?: unknown }
}

export default async function* (source: AsyncIterable<TestEvent>): AsyncGenerator<string> {
  for await (const event of source) {
    if (event.type !== 'test:pass' && event.type !== 'test:fail') continue
    const { name, file } = event.data
    if (typeof name === 'string' && typeof file === 'string') {
      yield JSON.stringify({ name, file: relative(process.cwd(), file) }) + '\n'
    }
  }
}
