// A box desktop's live view: noVNC over the engine's WebSocket to the box's Xvnc (spec 5.14). Xvnc sends only the
// regions that changed, the view asks for each picture, and noVNC draws the cursor itself.
//
// The view starts view only; the caller turns control on. While the picture moves it streams at JPEG quality 6, and
// after 1.5 s still it asks for quality 9 and a full redraw, so still text is sharp: v1's Auto mode, ported from its
// computerStream.ts with its rules unchanged. noVNC has no API for this, so the file reads private members of the pinned
// version (1.7.0): rfb._display, rfb._decoders, rfb._sendEncodings and RFB.messages. Tests cover the encoding switch.

import type { RfbDecoder, RfbMessages } from "@novnc/novnc";

type RfbClass = typeof import("@novnc/novnc").default;
export type Rfb = InstanceType<RfbClass>;

// The RFB pseudo-encoding that asks the server to push frames. The view leaves it out, so it requests each one and a slow
// link gets fewer frames rather than a queue.
const continuousUpdates = -313;
const tight = 7;

let loading: Promise<RfbClass> | undefined;

// noVNC probes H.264 decoding with a top-level await as it loads, and a browser whose probe throws fails the import for
// good. Xvnc sends no H.264, so VideoDecoder is hidden while noVNC loads, and the probe finds none. A failed load is tried
// again by the next view.
export function loadRFB(): Promise<RfbClass> {
  loading ??= (async () => {
    const decoder = Object.getOwnPropertyDescriptor(window, "VideoDecoder");
    try { if (decoder) Reflect.deleteProperty(window, "VideoDecoder"); } catch { /* the probe then runs */ }
    try {
      const RFB = (await import("@novnc/novnc")).default;
      const send = RFB.messages.clientEncodings;
      RFB.messages.clientEncodings = (sock, encodings) => send.call(RFB.messages, sock, encodings.filter(e => e !== continuousUpdates));
      return RFB;
    } finally {
      if (decoder && !("VideoDecoder" in window)) Object.defineProperty(window, "VideoDecoder", decoder);
    }
  })();
  loading.catch(() => { loading = undefined; });
  return loading;
}

// AutoQuality is the still-picture detector autoQuality returns.
export interface AutoQuality {
  refreshing(): void;
  refreshed(time: number): void;
  scroll(time: number): void;
  region(x: number, y: number, width: number, height: number, screenWidth: number, screenHeight: number): void;
  sample(fraction: number, time: number): void;
}

// Two changed samples within 300 ms mean motion. One sample can be a sharper redraw. Ignore small cursor changes. A
// still picture gets 1.5 s.
export function autoQuality(change: (smooth: boolean) => void, now: number = performance.now()): AutoQuality {
  let smooth = true;
  let lastMotion = now;
  let sampledAt = now;
  let candidate = -Infinity;
  let sharpAt = -Infinity;
  let regionAt = -Infinity;
  let refreshing = false;
  let area = 0, left = 1, top = 1, right = 0, bottom = 0;
  return {
    refreshing() { refreshing = true; },
    refreshed(time) {
      refreshing = false;
      sharpAt = time;
      candidate = regionAt = -Infinity;
      area = 0; left = 1; top = 1; right = 0; bottom = 0;
    },
    scroll(time) {
      refreshing = false;
      lastMotion = time;
      if (!smooth) { smooth = true; change(true); }
    },
    region(x, y, width, height, screenWidth, screenHeight) {
      if (!screenWidth || !screenHeight) return;
      area += width * height / (screenWidth * screenHeight);
      left = Math.min(left, x / screenWidth);
      top = Math.min(top, y / screenHeight);
      right = Math.max(right, (x + width) / screenWidth);
      bottom = Math.max(bottom, (y + height) / screenHeight);
    },
    sample(fraction, time) {
      const fresh = time - sampledAt <= 300;
      sampledAt = time;
      // Combine tiled updates. Identical terminal lines may only change the tall strip of row numbers along the left edge.
      const width = right - left, height = bottom - top;
      const broad = (area >= 0.2 && width >= 0.6 && height >= 0.25)
        || (area >= 0.005 && left <= 0.15 && width >= 0.015 && height >= 0.6);
      // Require two broad updates within 300 ms, rather than counting each one.
      const scrolling = broad && time - regionAt <= 300;
      if (broad) regionAt = time;
      area = 0; left = 1; top = 1; right = 0; bottom = 0;
      // The full sharp redraw can arrive in several pieces.
      if (!smooth && (refreshing || time - sharpAt < 300)) return;
      if (fraction >= 0.008 || scrolling) {
        lastMotion = time;
        if (!smooth && (scrolling || time - candidate <= 300)) {
          smooth = true;
          change(true);
        }
        candidate = time;
      } else if (smooth && fresh && time - lastMotion >= 1500) {
        smooth = false;
        sharpAt = time;
        candidate = -Infinity;
        change(false);
      }
    },
  };
}

// EncodingTarget is the part of a noVNC client setEncodingMode writes.
export interface EncodingTarget {
  _sock: object;
  _rfbConnectionState: string;
  _fbWidth: number;
  _fbHeight: number;
  _sendEncodings(): void;
  qualityLevel: number;
  compressionLevel: number;
}

// noVNC 1.7.0 keeps the canvas and connection when sending SetEncodings. A full update gives still text its sharp pixels
// without waiting for damage.
export function setEncodingMode(rfb: EncodingTarget, smooth: boolean): void {
  rfb.qualityLevel = smooth ? 6 : 9;
  rfb.compressionLevel = smooth ? 2 : 4;
  if (rfb._rfbConnectionState !== "connected") return;
  rfb._sendEncodings();
  if (!smooth) {
    // noVNC keeps its message writers on the class, which the instance does not type.
    const messages = (rfb.constructor as Function & { messages: RfbMessages }).messages;
    messages.fbUpdateRequest(rfb._sock, false, 0, 0, rfb._fbWidth, rfb._fbHeight);
  }
}

// DesktopStream is one view's connection, which openStream returns at once.
export interface DesktopStream {
  // control turns the owner's keyboard and pointer on or off. View only is the default.
  control(on: boolean): void;
  // key sends one key from the phone keyboard.
  key(keysym: number, down: boolean): void;
  close(): void;
}

export type StreamEvents = { live(width: number, height: number): void; ended(): void };

// openStream connects target to url and reports once it shows the desktop, and once it ends for any reason. The view
// decides what an end means; nothing reconnects on its own.
export function openStream(target: HTMLElement, url: string, on: StreamEvents): DesktopStream {
  let rfb: Rfb | undefined;
  let closed = false;
  let control = false;
  let sampler: ReturnType<typeof setInterval> | undefined;
  let quality: AutoQuality | undefined;
  // The sharp redraw asked for, until its Tight rectangles are painted.
  let refresh: { tight: boolean } | undefined;
  const scrollKeys = new Set(["ArrowUp", "ArrowDown", "PageUp", "PageDown", "Home", "End", " "]);
  // The owner's own scrolling is motion at once, before any picture changes.
  const scrolled = (event: Event) => {
    if (event.type !== "keydown" || scrollKeys.has((event as KeyboardEvent).key)) quality?.scroll(performance.now());
  };
  for (const type of ["wheel", "touchmove", "keydown"]) target.addEventListener(type, scrolled, { capture: true, passive: true });

  // A 64 x 40 copy of the canvas, every 100 ms: the share of it that changed is the motion autoQuality reads.
  function watchPicture(mine: Rfb) {
    const pixels = document.createElement("canvas").getContext("2d", { willReadFrequently: true });
    if (!pixels) return;
    pixels.canvas.width = 64;
    pixels.canvas.height = 40;
    let before: Uint8ClampedArray | undefined;
    const q = autoQuality(smooth => {
      if (!smooth) { q.refreshing(); refresh = { tight: false }; } else refresh = undefined;
      setEncodingMode(mine, smooth);
    });
    quality = q;
    sampler = setInterval(() => {
      const canvas = target.querySelector("canvas");
      if (mine !== rfb || mine._rfbConnectionState !== "connected" || !canvas?.width) return;
      try {
        pixels.drawImage(canvas, 0, 0, 64, 40);
        const after = pixels.getImageData(0, 0, 64, 40).data;
        if (before) {
          let changed = 0;
          for (let i = 0; i < after.length; i += 4) {
            if (Math.max(Math.abs(after[i] - before[i]), Math.abs(after[i + 1] - before[i + 1]), Math.abs(after[i + 2] - before[i + 2])) > 12) changed++;
          }
          q.sample(changed / (64 * 40), performance.now());
        }
        before = after;
      } catch {
        // A browser without readable canvas pixels keeps quality 6.
        clearInterval(sampler);
      }
    }, 100);
  }

  // Each decoded rectangle feeds autoQuality's regions, and the first paint after the sharp redraw's Tight rectangles
  // ends the redraw. It never throws: without the hooks the view only stays at quality 6.
  function instrument(mine: Rfb) {
    try {
      for (const [encoding, decoder] of Object.entries(mine._decoders) as [string, RfbDecoder][]) {
        const decode = decoder.decodeRect.bind(decoder);
        decoder.decodeRect = (x, y, width, height, ...rest) => {
          const done = decode(x, y, width, height, ...rest);
          if (done) {
            quality?.region(x, y, width, height, mine._fbWidth, mine._fbHeight);
            if (Number(encoding) === tight && refresh) refresh.tight = true;
          }
          return done;
        };
      }
      const display = mine._display;
      const flip = display.flip.bind(display);
      display.flip = (...args) => {
        const result = flip(...args);
        if (refresh?.tight) {
          const ended = quality;
          refresh = undefined;
          void display.flush().then(() => { if (mine === rfb && quality === ended) quality?.refreshed(performance.now()); });
        }
        return result;
      };
    } catch { /* noVNC changed inside; the picture stays smooth */ }
  }

  const end = () => {
    clearInterval(sampler);
    if (!closed) { closed = true; on.ended(); }
  };
  void loadRFB().then(RFB => {
    if (closed) return;
    const mine = rfb = new RFB(target, url, { shared: true });
    instrument(mine);
    mine.viewOnly = !control;
    mine.focusOnClick = control;
    mine.scaleViewport = true;
    mine.clipViewport = false;
    mine.resizeSession = false;
    mine.showDotCursor = false;
    mine.background = "transparent";
    setEncodingMode(mine, true);
    mine.addEventListener("connect", () => {
      if (mine !== rfb || closed) return;
      watchPicture(mine);
      on.live(mine._fbWidth, mine._fbHeight);
    });
    mine.addEventListener("disconnect", end);
    mine.addEventListener("securityfailure", end);
    mine.addEventListener("credentialsrequired", () => { mine.disconnect(); });
  }, end);

  return {
    control(on) {
      control = on;
      if (!rfb) return;
      rfb.viewOnly = !on;
      rfb.focusOnClick = on;
      if (on) rfb.focus({ preventScroll: true });
      else rfb.blur();
    },
    key(keysym, down) { if (control) rfb?.sendKey(keysym, null, down); },
    close() {
      closed = true;
      clearInterval(sampler);
      for (const type of ["wheel", "touchmove", "keydown"]) target.removeEventListener(type, scrolled, true);
      try { rfb?.disconnect(); } catch { /* already gone */ }
      rfb = undefined;
    },
  };
}
