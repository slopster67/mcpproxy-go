// TS port of internal/secret/refname.go RefName; keeps the field KIND
// (env vs header) in the name so an env var and a header of the same name
// never collide on one secret entry. Must agree with the Go and Swift ports.

const REF_NAME_MAX_LEN = 64

function normalizeRefComponent(s: string): string {
  const lower = s.toLowerCase()
  const collapsed = lower.replace(/[^a-z0-9-]+/g, '-')
  const dedupedHyphens = collapsed.replace(/-{2,}/g, '-')
  let trimmed = dedupedHyphens.replace(/^-+|-+$/g, '')
  if (trimmed.length > REF_NAME_MAX_LEN) {
    trimmed = trimmed.slice(0, REF_NAME_MAX_LEN).replace(/-+$/g, '')
  }
  return trimmed
}

/**
 * refName computes the encrypted secret entry name for one field on one server
 * "<server>-<kind>-<key>", lower-cased, with every run of
 * characters outside [a-z0-9-] collapsed to a single '-', trimmed, and
 * capped at 64 characters. kind is normally "env" or "header" — keeping it a
 * distinct path component (rather than folding it into key) is what keeps an
 * env var and a header of the same name from colliding on one entry.
 *
 * `taken` reports whether a candidate name is already present in the
 * secret (GET /secrets); on a collision refName appends -2, -3, …
 * until it finds a free one, so an add never silently overwrites an
 * existing secret.
 */
export function refName(
  server: string,
  kind: 'env' | 'header',
  key: string,
  taken?: (name: string) => boolean
): string {
  const base = normalizeRefComponent(`${server}-${kind}-${key}`)
  if (!taken) return base

  let candidate = base
  let n = 2
  while (taken(candidate)) {
    const suffix = `-${n}`
    const maxBase = REF_NAME_MAX_LEN - suffix.length
    const trimmedBase = base.length > maxBase ? base.slice(0, maxBase) : base
    candidate = trimmedBase + suffix
    n++
  }
  return candidate
}
