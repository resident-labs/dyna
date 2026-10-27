// The room's open questions and the visuals its posts carry. lib/questions
// holds the question rows and lib/files/visuals holds the visual set the feed
// and the Visuals view read; this module keeps the lists and the requests that
// keep them current, so App.svelte only hosts them.
import { api } from '../../api.ts'
import { reconcileRows } from '../ui/reloadGate.ts'
import { questionID, visualKey } from '../questions/questions.ts'
import { setVisuals } from '../files/visuals.svelte.ts'
import { router } from './router.svelte.ts'
import type { Post, Question, Visual } from '../api/room.ts'

/** The page's own side of the questions and visuals. App binds these once. */
export interface QuestionsHost {
  /** The page's reload gate, so a list keeps one request and one refresh. */
  reload(name: string, load: (current: () => boolean) => void | Promise<unknown>): Promise<void>
  /** Show a loader failure. */
  error(message: string): void
  /** Follow a link to another view. */
  openView(view: string): void
}

// App binds the page's side during its script, before the first effect runs.
const missing = (name: string): never => { throw new Error(`questions: ${name} is unbound`) }
let questionsHost: QuestionsHost = {
  reload: () => missing('reload'),
  error: () => missing('error'),
  openView: () => missing('openView'),
}

export function bindQuestions(bound: QuestionsHost): void {
  questionsHost = bound
}

const roomId = $derived(router.roomId)

let questions = $state<Question[]>([])
let visuals = $state<Visual[]>([])
let questionFocus = $state('')
let questionFocusRequest = $state(0)
let questionsRequest = 0

/**
 * loadQuestions reads the room's questions. A slow answer for an older room or
 * an older request never replaces a newer one.
 */
function loadQuestions() {
  return questionsHost.reload('questions', async (current: () => boolean) => {
    const request = ++questionsRequest
    const id = roomId
    if (!id) return
    try {
      const q = await api.questions(id)
      if (current() && id === roomId && request === questionsRequest) { questions = reconcileRows(questions, q?.[id] ?? []) }
    } catch (e) {
      if (current() && id === roomId && request === questionsRequest) questionsHost.error(String(e))
    }
  })
}

// loadVisuals rereads the visuals the posts carry and hands them to the set the
// feed and the Visuals view read.
function loadVisuals() {
  return questionsHost.reload('visuals', async (current: () => boolean) => {
    try {
      const list = await api.visuals(roomId)
      if (current()) {
        visuals = reconcileRows(visuals, list ?? [], visualKey)
        setVisuals(roomId, visuals)
      }
    } catch (e) {
      if (current()) questionsHost.error(String(e))
    }
  })
}

async function answerQuestion(id: string, choice: string, text: string) {
  await api.answerQuestion(roomId, id, choice, text)
  await loadQuestions()
}

// answerFromCard answers with text an HTML card wrote and the owner confirmed.
async function answerFromCard(id: string, choice: string, text: string) {
  await api.answerQuestionFromCard(roomId, id, choice, text)
  await loadQuestions()
}

// openQuestion opens the Questions view on the question this post asks.
function openQuestion(post: Post) {
  questionsHost.openView('questions')
  questionFocus = questionID(post)
  questionFocusRequest++
}

/** The room's questions and visuals, named as the page named them when they lived there. */
export const questionView = {
  get questions() { return questions },
  get visuals() { return visuals },
  get focus() { return questionFocus },
  get focusRequest() { return questionFocusRequest },
  loadQuestions,
  loadVisuals,
  answerQuestion,
  answerFromCard,
  openQuestion,
  /** A question change patches the list in place. */
  setQuestions(rows: Question[]) { questions = rows },
  /** A room pick drops the old room's questions and visuals. */
  clear() {
    questions = []
    visuals = []
    questionFocus = ''
    questionsRequest++
  },
}
