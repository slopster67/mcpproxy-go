import type { ApiError } from "@/services/api";

function errorMessage(
  err: unknown,
  fallback = "Something went wrong",
): string {
  if (err instanceof Error && err.message) return err.message;
  if (typeof err === "string" && err) return err;
  return fallback;
}

function isForbidden(err: unknown): boolean {
  return (err as ApiError | undefined)?.status === 403;
}

// A 403 anywhere reads as "not an administrator", never as an expired dashboard session.
export function describeError(err: unknown, fallback?: string): string {
  if (isForbidden(err)) return "Requires an administrator";
  return errorMessage(err, fallback);
}
