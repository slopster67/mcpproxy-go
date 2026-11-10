import type { InventoryTool } from "./toolInventory";
import { initialToolDescription, toolMarkdown } from "./toolMarkdown";
import { reviewDiffSections } from "./toolReviewPresentation";

// One tool per idle task: preparing a large inventory must not block input.
export function warmToolPresentation(tools: InventoryTool[]) {
  const priority = (tool: InventoryTool) =>
    tool.review?.previous ? 0 : tool.review ? 1 : 2;
  const queue = [...tools].sort((a, b) => priority(a) - priority(b));
  let index = 0,
    cancelled = false,
    cancelTask = () => {};
  function schedule() {
    if (cancelled || index >= queue.length) return;
    if (typeof window.requestIdleCallback === "function") {
      const id = window.requestIdleCallback(run, { timeout: 1000 });
      cancelTask = () => window.cancelIdleCallback(id);
    } else {
      const id = window.setTimeout(run, 16);
      cancelTask = () => window.clearTimeout(id);
    }
  }
  function run() {
    if (cancelled) return;
    const tool = queue[index++]!;
    if (tool.review?.previous) reviewDiffSections(tool.review);
    if (
      !tool.review?.previous ||
      tool.review.previous.description === tool.description
    )
      toolMarkdown(initialToolDescription(tool.description ?? ""));
    schedule();
  }
  schedule();
  return () => {
    cancelled = true;
    cancelTask();
  };
}
