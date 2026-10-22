// A goal's journal events (engine call GoalEvents), newest first.
// createGoalEvents returns open(room, goal), which loads the first page,
// and older(), which loads the next older page with the cursor. A late
// answer for another goal or an earlier open is dropped. set receives
// {items, older, cursor, loading, error}.
export interface GoalEvent {
  id: string
  goal: string
  item?: string
  by: string
  event: string
  text?: string
  at: string
}

export interface EventsState {
  items: GoalEvent[]
  older: boolean
  cursor: string
  loading: boolean
  error: string
}

export interface EventsApi {
  goalEvents: (room: string, goal: string, before: string, limit: number) => Promise<{ items?: GoalEvent[]; older?: boolean; cursor?: string }>
}

const PAGE = 20

export function createGoalEvents(api: EventsApi, set: (state: EventsState) => void) {
  let room = ''
  let goal = ''
  let visit = 0
  let state: EventsState = { items: [], older: false, cursor: '', loading: false, error: '' }
  const publish = (patch: Partial<EventsState>) => {
    state = { ...state, ...patch }
    set(state)
  }
  function load(before: string) {
    const mine = visit
    publish({ loading: true, error: '' })
    api.goalEvents(room, goal, before, PAGE).then(
      (page) => {
        if (mine !== visit) return
        const items = before ? [...state.items, ...(page.items ?? [])] : page.items ?? []
        publish({ items, older: !!page.older, cursor: page.cursor ?? '', loading: false })
      },
      (error: unknown) => {
        if (mine === visit) {
          const message = error instanceof Error ? error.message : String(error)
          publish({ loading: false, error: message })
        }
      },
    )
  }
  return {
    open(roomId: string, goalId: string) {
      room = roomId
      goal = goalId
      visit++
      state = { items: [], older: false, cursor: '', loading: false, error: '' }
      load('')
    },
    older() {
      if (state.older && !state.loading) load(state.cursor)
    },
  }
}
