// The microphone's AudioWorklet: it collects mono frames of about 40 ms and
// posts them to the page. The page encodes and sends them.
class DynaMic extends AudioWorkletProcessor {
  constructor() {
    super()
    this.buf = new Float32Array(Math.round(sampleRate * 0.04))
    this.n = 0
  }

  process(inputs) {
    const ch = inputs[0]?.[0]
    if (!ch) return true
    for (let i = 0; i < ch.length; i++) {
      this.buf[this.n++] = ch[i]
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
