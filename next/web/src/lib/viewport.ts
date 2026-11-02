// The app's page never scrolls; only its panes do. With a phone's keyboard open, iOS keeps the layout viewport tall and
// moves the visible area over it, so the page can still be dragged, or pushed to reveal the caret, even with no overflow.
// As in react-aria's usePreventScroll: a touch that no pane can use is cancelled, and a page that moved anyway goes back.
// interactive-widget in index.html spares Android this, as it shrinks the layout viewport with the keyboard.

type Box<T> = { parentElement: T | null; scrollHeight: number; clientHeight: number; scrollWidth: number; clientWidth: number };
type Overflow = { overflowX: string; overflowY: string };

const scrolls = /auto|scroll/;

// Pinch zoom shrinks the visible height too, and the reader pans while zoomed.
export const zoomed = (scale = 1) => Math.abs(scale - 1) > 0.01;

// Whether something from the touched box up to root has content to scroll. Only real overflow counts: Safari lets a drag
// on a pane that fits pass through to the page, whatever its overscroll-behavior (WebKit bug 243452).
export function canScroll<T extends Box<T>>(from: T | null, root: T, style: (box: T) => Overflow): boolean {
  for (let box = from; box; box = box === root ? null : box.parentElement) {
    const { overflowX, overflowY } = style(box);
    if ((box.scrollHeight > box.clientHeight && scrolls.test(overflowY)) || (box.scrollWidth > box.clientWidth && scrolls.test(overflowX))) return true;
  }
  return false;
}

export const panned = (viewport: { offsetTop: number; scale: number } | null | undefined, scrollY: number) =>
  !zoomed(viewport?.scale) && (scrollY > 0 || (viewport?.offsetTop ?? 0) > 0);

// A drag that starts on node moves the page only if something inside node can scroll. Two fingers still zoom, and a
// selection keeps its handles.
export function holdDrags(node: HTMLElement): () => void {
  const move = (event: TouchEvent) => {
    const target = event.target;
    if (!event.cancelable || event.touches.length > 1 || !(target instanceof Element)) return;
    if (target instanceof HTMLTextAreaElement && target.selectionStart !== target.selectionEnd) return;
    if (!canScroll(target, node, getComputedStyle)) event.preventDefault();
  };
  node.addEventListener("touchmove", move, { passive: false });
  return () => node.removeEventListener("touchmove", move);
}

// Attached to the shell, so a sign-in screen, which is a normal scrolling page, keeps its scroll.
export function pinPage(): () => void {
  const viewport = window.visualViewport;
  const pin = () => { if (panned(viewport, window.scrollY)) window.scrollTo(0, 0); };
  viewport?.addEventListener("scroll", pin);
  viewport?.addEventListener("resize", pin);
  window.addEventListener("scroll", pin, { passive: true });
  return () => {
    viewport?.removeEventListener("scroll", pin);
    viewport?.removeEventListener("resize", pin);
    window.removeEventListener("scroll", pin);
  };
}
