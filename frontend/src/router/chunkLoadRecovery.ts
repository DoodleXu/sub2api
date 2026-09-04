export const CHUNK_RELOAD_ATTEMPT_KEY = "chunk_reload_attempted";
export const CHUNK_RELOAD_COOLDOWN_MS = 10_000;

export type ChunkLoadRecoveryAction = "reload" | "fallback" | null;

export function isChunkLoadError(error: unknown): boolean {
  if (typeof error !== "object" || error === null) return false;

  const candidate = error as { message?: unknown; name?: unknown };
  const message = typeof candidate.message === "string" ? candidate.message : "";
  const name = typeof candidate.name === "string" ? candidate.name : "";

  return (
    message.includes("Failed to fetch dynamically imported module") ||
    message.includes("Loading chunk") ||
    message.includes("Loading CSS chunk") ||
    name === "ChunkLoadError"
  );
}

export function resolveChunkLoadRecoveryAction(
  error: unknown,
  lastReload: string | null,
  now = Date.now(),
): ChunkLoadRecoveryAction {
  if (!isChunkLoadError(error)) return null;

  const lastReloadAt = lastReload === null ? Number.NaN : Number(lastReload);
  if (
    !Number.isFinite(lastReloadAt) ||
    now - lastReloadAt > CHUNK_RELOAD_COOLDOWN_MS
  ) {
    return "reload";
  }
  return "fallback";
}

export function resolveChunkRetryPath(
  value: unknown,
  currentOrigin: string,
): string {
  if (typeof value !== "string") {
    return "/";
  }

  try {
    const origin = new URL(currentOrigin);
    const target = new URL(value, origin);
    if (target.origin !== origin.origin) return "/";
    return `${target.pathname}${target.search}${target.hash}`;
  } catch {
    return "/";
  }
}
