// The microphone's AudioWorklet: it collects mono frames of about 40 ms and
// posts them to the page. The page encodes and sends them.

// The worklet scope supplies these three names. TypeScript's DOM library
// describes the page, not the worklet, so the file declares them itself.
declare class AudioWorkletProcessor {
  readonly port: MessagePort
  constructor()
}
declare function registerProcessor(name: string, processor: new () => AudioWorkletProcessor): void
declare const sampleRate: number

class DynaMic extends AudioWorkletProcessor {
  buf: Float32Array
  n: number

  constructor() {
    super()
    this.buf = new Float32Array(Math.round(sampleRate * 0.04))
    this.n = 0
  }

  process(inputs: Float32Array[][]): boolean {
    const ch = inputs[0]?.[0]
    if (!ch) return true
    for (const sample of ch) {
      this.buf[this.n++] = sample
      if (this.n === this.buf.length) {
        this.port.postMessage(this.buf)
        this.buf = new Float32Array(this.buf.length)
        this.n = 0
      }
    }
    return true
  }
}

registerProcessor('dyna-mic', DynaMic)

export {}
