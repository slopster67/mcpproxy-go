type Style = Record<string, string>
export interface AnsiPart { text: string; style: Style }

const palette = [
  '#000000', '#cd0000', '#00cd00', '#cdcd00', '#0000ee', '#cd00cd', '#00cdcd', '#e5e5e5',
  '#7f7f7f', '#ff0000', '#00ff00', '#ffff00', '#5c5cff', '#ff00ff', '#00ffff', '#ffffff',
]

function indexedColor(value: number): string | undefined {
  if (!Number.isInteger(value) || value < 0 || value > 255) return
  if (value < 16) return palette[value]
  if (value >= 232) {
    const grey = 8 + (value - 232) * 10
    return `rgb(${grey},${grey},${grey})`
  }
  const n = value - 16
  const levels = [0, 95, 135, 175, 215, 255]
  return `rgb(${levels[Math.floor(n / 36)]},${levels[Math.floor(n / 6) % 6]},${levels[n % 6]})`
}

// Only SGR changes styling. Other terminal controls (including OSC links) are
// discarded; all printable output is rendered through Vue text interpolation.
export function parseAnsi(input: string): AnsiPart[] {
  const parts: AnsiPart[] = []
  let style: Style = {}
  const controls = /\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|(?:\x1b\[|\x9b)[0-?]*[ -/]*[@-~]|\x1b[@-_]|[\x00-\x08\x0b-\x1f\x7f]/g
  let offset = 0
  for (const match of input.matchAll(controls)) {
    if (match.index > offset) parts.push({ text: input.slice(offset, match.index), style: { ...style } })
    offset = match.index + match[0].length
    if (!/^(?:\x1b\[|\x9b)[\d;]*m$/.test(match[0])) continue
    const codes = match[0].replace(/^(?:\x1b\[|\x9b)|m$/g, '').split(';').map(Number)
    for (let i = 0; i < codes.length; i++) {
      const code = codes[i]!
      if (code === 0) style = {}
      else if (code === 1) style.fontWeight = 'bold'
      else if (code === 2) style.opacity = '0.6'
      else if (code === 3) style.fontStyle = 'italic'
      else if (code === 4) style.textDecoration = 'underline'
      else if (code === 9) style.textDecoration = 'line-through'
      else if (code === 22) { delete style.fontWeight; delete style.opacity }
      else if (code === 23) delete style.fontStyle
      else if (code === 24 || code === 29) delete style.textDecoration
      else if (code === 39) delete style.color
      else if (code === 49) delete style.backgroundColor
      else if (code >= 30 && code <= 37) style.color = palette[code - 30]!
      else if (code >= 90 && code <= 97) style.color = palette[code - 90 + 8]!
      else if (code >= 40 && code <= 47) style.backgroundColor = palette[code - 40]!
      else if (code >= 100 && code <= 107) style.backgroundColor = palette[code - 100 + 8]!
      else if (code === 38 || code === 48) {
        const mode = codes[++i]
        let color: string | undefined
        if (mode === 5) color = indexedColor(codes[++i]!)
        else if (mode === 2) {
          const rgb = codes.slice(i + 1, i + 4)
          i += 3
          if (rgb.length === 3 && rgb.every(v => Number.isInteger(v) && v >= 0 && v <= 255)) color = `rgb(${rgb.join(',')})`
        }
        if (color) style[code === 38 ? 'color' : 'backgroundColor'] = color
      }
    }
  }
  if (offset < input.length) parts.push({ text: input.slice(offset), style: { ...style } })
  return parts
}
