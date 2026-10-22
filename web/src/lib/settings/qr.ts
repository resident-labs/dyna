// QR codes drawn in the page, so a pairing link never goes to a QR service.
import qrcode from 'qrcode-generator'

// qrPath encodes text and returns {size, d}: an SVG path of the dark
// modules on a size x size grid, with no quiet zone (the caller pads).
export function qrPath(text: string): { size: number; d: string } {
  const qr = qrcode(0, 'M')
  qr.addData(text)
  qr.make()
  const size = qr.getModuleCount()
  let d = ''
  for (let r = 0; r < size; r++) {
    for (let c = 0; c < size; c++) {
      if (!qr.isDark(r, c)) continue
      // Join a run of dark modules in a row into one rectangle.
      let w = 1
      while (c + w < size && qr.isDark(r, c + w)) w++
      d += `M${c} ${r}h${w}v1h-${w}z`
      c += w - 1
    }
  }
  return { size, d }
}
