export function documentCopy(root: HTMLElement, write: (text: string) => Promise<void>): () => void {
  const document = root.ownerDocument
  const cleanups: (() => void)[] = []
  for (const code of root.querySelectorAll('pre > code')) {
    const pre = code.parentElement
    if (!pre) continue
    const wrapper = document.createElement('div')
    wrapper.className = 'document-code'
    pre.before(wrapper)
    wrapper.append(pre)
    const button = document.createElement('button')
    button.type = 'button'
    button.className = 'document-copy'
    button.textContent = 'Copy code'
    button.setAttribute('aria-label', 'Copy code')
    const status = document.createElement('span')
    status.className = 'document-copy-status'
    status.setAttribute('role', 'status')
    wrapper.append(button, status)
    let disposed = false
    let generation = 0
    let timer: ReturnType<typeof setTimeout> | undefined
    const reset = () => {
      button.textContent = 'Copy code'
      status.textContent = ''
      status.classList.remove('error')
    }
    const copy = async () => {
      const current = ++generation
      clearTimeout(timer)
      reset()
      try {
        await write(code.textContent ?? '')
        if (disposed || current !== generation) return
        button.textContent = '✓ Copied'
        status.textContent = 'Copied code'
      } catch {
        if (disposed || current !== generation) return
        status.textContent = 'Could not copy'
        status.classList.add('error')
      }
      timer = setTimeout(reset, 2000)
    }
    button.addEventListener('click', copy)
    cleanups.push(() => {
      disposed = true
      clearTimeout(timer)
      button.removeEventListener('click', copy)
      wrapper.replaceWith(pre)
    })
  }
  return () => cleanups.forEach((cleanup) => cleanup())
}
