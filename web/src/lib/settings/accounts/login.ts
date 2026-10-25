// The sign-in flow of the Accounts page. An account is one provider sign-in, so
// the page starts Pi's provider sign-in and Dyna saves one account for each
// provider the person signs in to.
import type { PrivateLogin } from '../../api/harness.ts'

// LoginFlow is a sign-in card that an account waits on. The page keeps it while
// the add dialog is open or the owner returns to the page.
export type LoginFlow = PrivateLogin & { harness: string }

// piSignInID is the account ID of Pi's provider sign-in card, which has no
// account of its own. Source: piProviderSignInID in internal/harness/runtime.
export const piSignInID = 'pi:provider'

// recoveredLogins gives each saved flow its harness, so a reload can show a
// sign-in that is already running. A flow names its own harness when it has no
// account of its own, such as Pi's provider sign-in.
export function recoveredLogins<F extends { accountId: string; harness?: string }>(flows: F[] | null | undefined, accounts: { id: string; harness: string }[] | null | undefined): (F & { harness: string })[] {
  return (flows ?? []).flatMap((flow) => {
    const harness = flow.harness || (accounts ?? []).find((account) => account.id === flow.accountId)?.harness
    return harness ? [{ ...flow, harness }] : []
  })
}

// signInStatus words the step of an in-app sign-in for the shared card. A
// failure repeats the engine's message, which names the missing step.
const SIGN_IN_STATUS: Record<string, string> = {
  waiting: 'Waiting for you to approve the sign-in.',
  verifying: 'Checking the sign-in…',
  succeeded: 'Signed in.',
  cancelled: 'Sign-in cancelled.',
}
export function signInStatus(state?: { phase?: string; message?: string } | null): string {
  if (state?.phase === 'waiting' && state.message) return state.message
  if (state?.phase === 'failed') return state.message || 'Sign-in did not finish. Try again.'
  return SIGN_IN_STATUS[state?.phase ?? ''] ?? 'Starting sign-in…'
}
