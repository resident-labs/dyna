// createSelection is a view's one selected post id (postSelect.ts), kept in
// reactive state so every row of the view reads the same value.
import type { PostSelection } from './postSelect.ts'

export function createSelection(): PostSelection {
  let id = $state('')
  return {
    get: () => id,
    set: (next: string) => {
      id = next
    },
  }
}
