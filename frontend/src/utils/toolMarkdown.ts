import { marked } from "marked";
import DOMPurify from "dompurify";

export const toolDescriptionLimit = 1200;
const cache = new Map<string, string>();
export function toolMarkdown(text: string): string {
  const existing = cache.get(text);
  if (existing !== undefined) return existing;
  const html = DOMPurify.sanitize(
    marked.parse(text, { async: false, gfm: true }),
    {
      USE_PROFILES: { html: true },
      FORBID_TAGS: ["style", "form"],
      FORBID_ATTR: ["style"],
    },
  );
  if (cache.size >= 512) cache.delete(cache.keys().next().value!);
  cache.set(text, html);
  return html;
}
export function initialToolDescription(
  text: string,
  limit = toolDescriptionLimit,
) {
  return text.length > limit ? `${text.slice(0, limit)}…` : text;
}
