export function jsonTokens(
  text: string,
): Array<{ text: string; color?: string }> {
  const result: Array<{ text: string; color?: string }> = [];
  const pattern =
    /("(?:\\.|[^"\\])*")(\s*:)?|(-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)|\b(true|false|null)\b/g;
  let offset = 0;
  for (const match of text.matchAll(pattern)) {
    if (match.index > offset)
      result.push({ text: text.slice(offset, match.index) });
    result.push({
      text: match[0],
      color: match[2]
        ? "text-sky-300"
        : match[1]
          ? "text-emerald-300"
          : match[3]
            ? "text-orange-300"
            : match[4] === "null"
              ? "text-rose-300"
              : "text-violet-300",
    });
    offset = match.index + match[0].length;
  }
  if (offset < text.length) result.push({ text: text.slice(offset) });
  return result;
}
