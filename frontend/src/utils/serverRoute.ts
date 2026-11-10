// Registry server names may contain '/' (e.g. "io.github.owner/repo"). The
// server-detail route is a single `:serverName` segment, so the name is
// percent-encoded; vue-router decodes the param back to the original name.
export function serverDetailPath(name: string, tab?: string): string {
  const base = `/servers/${encodeURIComponent(name)}`;
  return tab ? `${base}?tab=${encodeURIComponent(tab)}` : base;
}

function toolsPath(
  server?: string,
  needsReview = false,
  tool?: string,
): string {
  const query = new URLSearchParams();
  if (server) query.set("server", server);
  if (needsReview) query.set("review", "1");
  if (tool) {
    query.set("q", tool);
    query.set("focus", tool);
  }
  return (
    "/tools" +
    (query.size ? "?" + query.toString() : "") +
    (server ? "#" + encodeURIComponent("tools-" + server) : "")
  );
}

export function reviewPath(name?: string): string {
  return toolsPath(name, true);
}

// serverDisplayName prefers the registry-provided human-friendly `title` over
// the raw reverse-DNS `name` identifier (e.g. "io.github.owner/repo"). The
// `name` remains the stable key used for API calls and routing; only the
// rendered label changes.
export function serverDisplayName(server: {
  name: string;
  title?: string;
}): string {
  return server.title?.trim() || server.name;
}
