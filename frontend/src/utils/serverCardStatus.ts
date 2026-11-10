import type { Server } from "@/types";
import { needsOAuthSignIn, healthStatusText } from "@/utils/health";

interface ServerCardStatus {
  order: 0 | 1 | 2 | 3;
  color: string;
  label: string;
  error?: string;
}

export function serverCardStatus(server: Server): ServerCardStatus {
  if (!server.enabled)
    return { order: 3, color: "bg-white", label: "Disabled" };
  if (server.host_id && server.host_online === false) return {order:3,color:'bg-white',label:`${server.host_name || 'Peer'} is offline`};
  const health = server.health;
  const signInState = needsOAuthSignIn(server);
  const failed =
    signInState ||
    !server.connected ||
    health?.level === "unhealthy" ||
    ["error", "sign_in_required", "needs_secret", "needs_config"].includes(
      health?.status ?? "",
    );
  if (failed) {
    let error = "Server error";
    if (signInState || health?.status === "sign_in_required")
      error = "Needs sign-in";
    else if (health?.status === "needs_secret") error = "Missing secret";
    else if (health?.status === "needs_config") error = "Needs configuration";
    else if (server.connecting || health?.status === "connecting")
      error = server.protocol === "stdio" ? "Starting…" : "Connecting…";
    else if (!server.connected)
      error =
        server.protocol === "stdio" ? "Failed to start" : "Failed to connect";
    return {
      error,
      order: 0,
      color: "bg-[var(--server-status-red)]",
      label:
        health?.detail ||
        server.diagnostic?.user_message ||
        server.last_error ||
        (signInState
          ? "Sign-in required"
          : health
            ? healthStatusText(health, server.connected)
            : "") ||
        "Connection problem",
    };
  }
  const reviewCount =
    (server.quarantine?.pending_count ?? 0) +
    (server.quarantine?.changed_count ?? 0);
  if (reviewCount > 0)
    return {
      order: 1,
      color: "bg-[var(--server-status-yellow)]",
      label: `Connected · ${reviewCount} tools awaiting approval`,
    };
  if (health?.level === "degraded")
    return {
      order: 0,
      color: "bg-[var(--server-status-red)]",
      label: health.summary || "Server needs attention",
      error: "Needs attention",
    };
  return {
    order: 2,
    color: "bg-[var(--server-status-green)]",
    label: "Connected",
  };
}
