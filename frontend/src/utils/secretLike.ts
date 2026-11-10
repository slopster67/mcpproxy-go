// Shared "does this field name look like a credential?" rule (ported verbatim from internal/secretlike/secretlike.go so
// Go, this file and the Swift port (CatalogTests.swift) default the
// Value/Secret toggle to Secret for the same field names.
const SECRET_LIKE_PATTERN =
  /(token|secret|password|passwd|api[_-]?key|[_-]key$|auth|credential|private[_-]?key)/i

export function looksSecret(name: string): boolean {
  return SECRET_LIKE_PATTERN.test(name)
}
