// The pinned noVNC client (1.7.0) ships no type declarations. This file
// declares the members computerStream.ts uses, including the private ones it
// reads on purpose (rfb._display, rfb._decoders, rfb._sendEncodings and
// RFB.messages). Tests and the live computer check cover the encoding switch.
declare module '@novnc/novnc' {
  // RfbMessages is the client's message writer (RFB.messages).
  export interface RfbMessages {
    clientEncodings(sock: object, encodings: number[]): void
    fbUpdateRequest(sock: object, incremental: boolean, x: number, y: number, width: number, height: number): void
  }

  // RfbDisplay owns the canvas and calls flip after each painted frame.
  export interface RfbDisplay {
    flip(...args: unknown[]): unknown
    pending(): boolean
    flush(): Promise<void>
  }

  // RfbDecoder decodes one encoding (rfb._decoders[encoding]).
  export interface RfbDecoder {
    decodeRect(x: number, y: number, width: number, height: number, ...rest: unknown[]): boolean
  }

  // RfbEvent is the detail noVNC passes to a connection listener.
  export interface RfbEvent {
    detail?: { clean?: boolean; reason?: string; status?: { reason?: string } }
  }

  export interface RfbOptions {
    shared?: boolean
  }

  export default class RFB {
    static messages: RfbMessages
    _sock: object
    _rfbConnectionState: string
    _fbWidth: number
    _fbHeight: number
    _display: RfbDisplay
    _decoders: Record<string, RfbDecoder>
    qualityLevel: number
    compressionLevel: number
    viewOnly: boolean
    focusOnClick: boolean
    scaleViewport: boolean
    clipViewport: boolean
    resizeSession: boolean
    showDotCursor: boolean
    background: string
    constructor(target: Element, urlOrChannel: string | WebSocket, options?: RfbOptions)
    _sendEncodings(): void
    focus(): void
    blur(): void
    disconnect(): void
    sendKey(keysym: number, code: string | null, down?: boolean): void
    addEventListener(name: string, handler: (event: RfbEvent) => void): void
  }
}
