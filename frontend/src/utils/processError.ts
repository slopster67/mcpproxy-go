/** Extract the captured stderr lines from the server's connection error. */
export function capturedStderr(error?: string): string {
  return (error ?? "")
    .split(/\r?\n/)
    .flatMap((line) => {
      const match = line.match(/^\s*\| ?(.*)$/);
      return match ? [match[1]!] : [];
    })
    .join("\n")
    .replace(/: transport error: transport closed$/, "");
}
