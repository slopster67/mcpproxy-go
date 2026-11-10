import { diffArrays } from "diff";

export type DiffKind = "context" | "removed" | "added";
export interface DiffFragment {
  text: string;
  changed: boolean;
}
export interface DiffLine {
  kind: DiffKind;
  text: string;
  fragments?: DiffFragment[];
}
export interface DiffBlock {
  hidden: boolean;
  lines: DiffLine[];
}

function edits(
  before: string[],
  after: string[],
): Array<{ kind: DiffKind; text: string }> {
  return diffArrays(before, after).flatMap((change) =>
    change.value.map((text) => ({
      kind: change.removed ? "removed" : change.added ? "added" : "context",
      text,
    })),
  );
}

function wordFragments(before: string, after: string) {
  const words = (text: string) =>
    text.match(/\s+|[\p{L}\p{N}_]+|[^\s\p{L}\p{N}_]/gu) ?? [];
  const changes = edits(words(before), words(after));
  const fragments = (omit: DiffKind) =>
    changes
      .filter((part) => part.kind !== omit)
      .map((part) => ({ text: part.text, changed: part.kind !== "context" }));
  return { before: fragments("added"), after: fragments("removed") };
}

export function diffLines(
  before: string,
  after: string,
  highlightWords = false,
): DiffLine[] {
  if (before === after) return [];
  const lines: DiffLine[] = edits(
    before === "" ? [] : before.split("\n"),
    after === "" ? [] : after.split("\n"),
  );
  if (highlightWords) {
    for (let i = 0; i < lines.length;) {
      if (lines[i]!.kind !== "removed") {
        i++;
        continue;
      }
      const removed: DiffLine[] = [],
        added: DiffLine[] = [];
      while (i < lines.length && lines[i]!.kind === "removed")
        removed.push(lines[i++]!);
      while (i < lines.length && lines[i]!.kind === "added")
        added.push(lines[i++]!);
      for (let n = 0; n < Math.min(removed.length, added.length); n++) {
        const fragments = wordFragments(removed[n]!.text, added[n]!.text);
        removed[n]!.fragments = fragments.before;
        added[n]!.fragments = fragments.after;
      }
    }
  }
  return lines;
}

export function foldContext(lines: DiffLine[], context = 3): DiffBlock[] {
  const blocks: DiffBlock[] = [];
  for (let i = 0; i < lines.length;) {
    if (lines[i]!.kind !== "context") {
      blocks.push({ hidden: false, lines: [lines[i++]!] });
      continue;
    }
    const start = i;
    while (i < lines.length && lines[i]!.kind === "context") i++;
    const run = lines.slice(start, i);
    const leading = start === 0 ? 0 : context,
      trailing = i === lines.length ? 0 : context;
    if (run.length <= leading + trailing + 1)
      blocks.push({ hidden: false, lines: run });
    else {
      if (leading) blocks.push({ hidden: false, lines: run.slice(0, leading) });
      blocks.push({
        hidden: true,
        lines: run.slice(leading, run.length - trailing),
      });
      if (trailing) blocks.push({ hidden: false, lines: run.slice(-trailing) });
    }
  }
  return blocks;
}

export function schemaText(value: unknown): string {
  if (value === undefined || value === null || value === "") return "";
  if (typeof value === "string") {
    try {
      value = JSON.parse(value);
    } catch {
      return value as string;
    }
  }
  function ordered(value: unknown): unknown {
    if (Array.isArray(value)) return value.map(ordered);
    if (value && typeof value === "object")
      return Object.fromEntries(
        Object.entries(value)
          .sort(([a], [b]) => a.localeCompare(b))
          .map(([key, item]) => [key, ordered(item)]),
      );
    return value;
  }
  return JSON.stringify(ordered(value), null, 2) ?? "";
}
