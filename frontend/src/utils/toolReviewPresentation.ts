import type { ReviewTool } from "@/types/api";
import { diffLines, foldContext, schemaText } from "./definitionDiff";

function behavior(value: Record<string, unknown> | null | undefined) {
  return {
    "Read-only": value?.readOnlyHint ?? false,
    "May delete or overwrite data": value?.destructiveHint ?? true,
    "Safe to repeat": value?.idempotentHint ?? false,
    "Uses external services": value?.openWorldHint ?? true,
  };
}
function buildSections(tool: ReviewTool) {
  const previous = tool.previous;
  if (!previous) return [];
  return [
    {
      title: "Description",
      before: previous.description ?? "",
      after: tool.description ?? "",
      json: false,
    },
    {
      title: "Input schema",
      before: schemaText(previous.input_schema),
      after: schemaText(tool.input_schema),
      json: true,
    },
    {
      title: "Output schema",
      before: schemaText(previous.output_schema),
      after: schemaText(tool.output_schema),
      json: true,
    },
    {
      title: "Behavior",
      before: schemaText(behavior(previous.annotations)),
      after: schemaText(behavior(tool.annotations)),
      json: true,
    },
  ].flatMap((section) => {
    const lines = diffLines(section.before, section.after, !section.json);
    return lines.length
      ? [
          {
            ...section,
            blocks: foldContext(lines),
            added: lines.filter((line) => line.kind === "added").length,
            removed: lines.filter((line) => line.kind === "removed").length,
          },
        ]
      : [];
  });
}
// Review objects are immutable snapshots. Reuse warmed results until the snapshot
// is replaced, then let WeakMap release the old presentation with its snapshot.
const cache = new WeakMap<ReviewTool, ReturnType<typeof buildSections>>();
export function reviewDiffSections(tool: ReviewTool) {
  let sections = cache.get(tool);
  if (!sections) {
    sections = buildSections(tool);
    cache.set(tool, sections);
  }
  return sections;
}
