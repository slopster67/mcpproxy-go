import type { ReviewTool } from '@/types'

// Bind each approval to the definitions displayed by the review.
export function expectedHashes(tools: ReviewTool[]): Record<string, string> {
  // Null prototype: tool names are upstream-controlled, and a tool named "__proto__" written into
  // a plain {} hits the inherited setter and silently drops out of the JSON (Sol review r1).
  const out: Record<string, string> = Object.create(null)
  for (const tool of tools) if (tool.current_hash) out[tool.name] = tool.current_hash
  return out
}
