// The pinned mammoth bundle (1.13.0) ships no type declarations. This file
// declares the one call the Word preview makes: convertToHtml on the browser
// build. The preview test and the viewer cover it.
declare module 'mammoth/mammoth.browser.js' {
  // MammothInput is the document bytes to convert.
  export interface MammothInput {
    arrayBuffer: ArrayBuffer
  }

  // MammothOptions are the conversion switches the preview sets.
  export interface MammothOptions {
    externalFileAccess?: boolean
  }

  // MammothMessage is one warning or error mammoth reported; it still returns
  // the HTML it could read.
  export interface MammothMessage {
    type: string
    message: string
  }

  export interface MammothResult {
    value: string
    messages: MammothMessage[]
  }

  const mammoth: {
    convertToHtml(input: MammothInput, options?: MammothOptions): Promise<MammothResult>
  }

  export default mammoth
}
