// A clock that wakes at a deadline, so a view that stays open reports the change
// a deadline brings without a new read from the host. The deadline is a function
// so the wake follows a new reading, and NaN means no wake is due. After a wake
// it asks for the next deadline, so several deadlines need one clock.
export function deadlineClock(deadline: () => number): () => number {
  let now = $state(Date.now())
  $effect(() => {
    let timer: ReturnType<typeof setTimeout> | undefined
    const arm = () => {
      const at = deadline()
      const wait = at - Date.now()
      if (Number.isFinite(at) && wait > 0) timer = setTimeout(wake, Math.min(wait, 2_147_483_647))
    }
    const wake = () => {
      now = Date.now()
      arm()
    }
    now = Date.now()
    arm()
    return () => clearTimeout(timer)
  })
  return () => now
}
