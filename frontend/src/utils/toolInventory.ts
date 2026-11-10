import type { GlobalTool } from "@/types/api";

export type InventoryTool = GlobalTool;
export type ToolChange = {
  server: string;
  incarnation?: number;
  names: string[];
} & (
  | { kind: "approve" | "block" }
  | { kind: "availability"; enabled: boolean }
  | {
      kind: "classification";
      tier: GlobalTool["tier"];
      classification: GlobalTool["classification"];
    }
);

export function needsToolReview(tool: GlobalTool) {
  return (
    tool.approval_status === "pending" || tool.approval_status === "changed"
  );
}
