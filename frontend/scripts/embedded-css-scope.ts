/**
 * CSS scoping for the embedded (Plane-hosted) build.
 *
 * Every rule of the embedded artifact is prefixed below the versioned root
 * selector `[data-weknora-embedded="v1"]` so the remote can never restyle
 * Plane or the page. Root-ish leading compounds (`:root`, `html`, `body`,
 * `*`, plus their `[theme-mode]` compounds) are replaced by the root
 * selector itself, which is how TDesign CSS variables and dark-mode token
 * blocks apply on the embedded root element instead of `documentElement`.
 *
 * This module is dependency-free so it can be unit-tested directly.
 */

export const EMBEDDED_ROOT_SELECTOR = '[data-weknora-embedded="v1"]'

const ROOT_TYPE_TOKENS = new Set(['html', 'body', '*'])

function matchBracket(source: string, start: number, open: string, close: string): number {
  let depth = 0
  let quote: string | null = null
  for (let i = start; i < source.length; i += 1) {
    const char = source[i]
    if (quote) {
      if (char === '\\') i += 1
      else if (char === quote) quote = null
      continue
    }
    if (char === '"' || char === "'") {
      quote = char
      continue
    }
    if (char === open) depth += 1
    else if (char === close) {
      depth -= 1
      if (depth === 0) return i
    }
  }
  return -1
}

function splitLeadingCompound(selector: string): { compound: string; rest: string } {
  let i = 0
  while (i < selector.length) {
    const char = selector[i]
    if (char === '[') {
      const end = matchBracket(selector, i, '[', ']')
      if (end < 0) return { compound: selector, rest: '' }
      i = end + 1
      continue
    }
    if (char === '(') {
      const end = matchBracket(selector, i, '(', ')')
      if (end < 0) return { compound: selector, rest: '' }
      i = end + 1
      continue
    }
    if (/\s/.test(char) || char === '>' || char === '~' || char === '+') break
    i += 1
  }
  return { compound: selector.slice(0, i), rest: selector.slice(i) }
}

interface CompoundToken {
  kind: 'type' | 'universal' | 'pseudo' | 'attr' | 'class' | 'id'
  text: string
}

function tokenizeCompound(compound: string): CompoundToken[] {
  const tokens: CompoundToken[] = []
  let i = 0
  while (i < compound.length) {
    const char = compound[i]
    if (char === '.') {
      let j = i + 1
      while (j < compound.length && /[-\w]/.test(compound[j])) j += 1
      tokens.push({ kind: 'class', text: compound.slice(i, j) })
      i = j
      continue
    }
    if (char === '#') {
      let j = i + 1
      while (j < compound.length && /[-\w]/.test(compound[j])) j += 1
      tokens.push({ kind: 'id', text: compound.slice(i, j) })
      i = j
      continue
    }
    if (char === '[') {
      const end = matchBracket(compound, i, '[', ']')
      if (end < 0) break
      tokens.push({ kind: 'attr', text: compound.slice(i, end + 1) })
      i = end + 1
      continue
    }
    if (char === ':') {
      let j = i + 1
      if (compound[j] === ':') j += 1
      while (j < compound.length && /[-\w]/.test(compound[j])) j += 1
      if (compound[j] === '(') {
        const end = matchBracket(compound, j, '(', ')')
        if (end > 0) j = end + 1
      }
      tokens.push({ kind: 'pseudo', text: compound.slice(i, j) })
      i = j
      continue
    }
    // type selector, universal selector, or a stray character
    if (char === '*') {
      tokens.push({ kind: 'universal', text: '*' })
      i += 1
      continue
    }
    let j = i
    while (j < compound.length && /[-\w]/.test(compound[j])) j += 1
    if (j > i) {
      const text = compound.slice(i, j)
      tokens.push({ kind: 'type', text })
      i = j
      continue
    }
    i += 1
  }
  return tokens
}

const isThemeAttr = (token: CompoundToken) => token.kind === 'attr' && /^\[theme-mode/i.test(token.text)

/**
 * Scope one comma-free selector under the embedded root. Idempotent:
 * selectors already scoped are returned untouched.
 */
export function scopeSelectorPart(part: string, rootSelector: string = EMBEDDED_ROOT_SELECTOR): string {
  const selector = part.trim()
  if (!selector || selector.includes('data-weknora-embedded')) return selector
  const { compound, rest } = splitLeadingCompound(selector)
  const tokens = tokenizeCompound(compound)
  if (tokens.length === 0) {
    // Selector starts with a combinator or is empty — prefix defensively.
    return `${rootSelector} ${selector}`
  }
  const typeToken = tokens.find((t) => t.kind === 'type' || t.kind === 'universal')
  const isRootish =
    tokens.some((t) => t.kind === 'pseudo' && t.text === ':root') ||
    (tokens.length === 1 && isThemeAttr(tokens[0])) ||
    (typeToken !== undefined &&
      (typeToken.kind === 'universal' || ROOT_TYPE_TOKENS.has(typeToken.text)))
  if (!isRootish) {
    return `${rootSelector} ${selector}`
  }
  const themeAttrs = tokens.filter(isThemeAttr).map((t) => t.text).join('')
  const remainder = tokens
    .filter((t) => !isThemeAttr(t))
    .filter((t) => !(t.kind === 'pseudo' && t.text === ':root'))
    .filter((t) => !(t.kind === 'type' && ROOT_TYPE_TOKENS.has(t.text)))
    .filter((t) => t.kind !== 'universal')
    .map((t) => t.text)
    .join('')
  const rootCompound = `${rootSelector}${themeAttrs}${remainder}`
  if (tokens.length === 1 && tokens[0].kind === 'universal' && rest.trim() === '') {
    // Bare `*` must still style descendants of the root.
    return `${rootSelector}, ${rootSelector} *`
  }
  return `${rootCompound}${rest}`
}
