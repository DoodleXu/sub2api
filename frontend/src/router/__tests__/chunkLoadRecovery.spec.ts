import { describe, expect, it } from "vitest";

import {
  CHUNK_RELOAD_COOLDOWN_MS,
  isChunkLoadError,
  resolveChunkLoadRecoveryAction,
  resolveChunkRetryPath,
} from "../chunkLoadRecovery";

describe("chunk load recovery", () => {
  it("recognizes JavaScript and CSS chunk failures", () => {
    expect(
      isChunkLoadError(
        new Error("Failed to fetch dynamically imported module: /SettingsView.js"),
      ),
    ).toBe(true);
    expect(isChunkLoadError(new Error("Loading CSS chunk failed"))).toBe(true);
    expect(isChunkLoadError(new Error("request failed"))).toBe(false);
  });

  it("reloads once and then selects the visible fallback", () => {
    const now = 100_000;
    const error = Object.assign(new Error("Loading chunk failed"), {
      name: "ChunkLoadError",
    });

    expect(resolveChunkLoadRecoveryAction(error, null, now)).toBe("reload");
    expect(resolveChunkLoadRecoveryAction(error, String(now), now + 1)).toBe(
      "fallback",
    );
    expect(
      resolveChunkLoadRecoveryAction(
        error,
        String(now),
        now + CHUNK_RELOAD_COOLDOWN_MS + 1,
      ),
    ).toBe("reload");
  });

  it("allows only same-origin retry paths", () => {
    const origin = "https://sub2api.example";

    expect(resolveChunkRetryPath("/admin/settings?tab=features", origin)).toBe(
      "/admin/settings?tab=features",
    );
    expect(resolveChunkRetryPath("//example.com/steal", origin)).toBe("/");
    expect(resolveChunkRetryPath("/\\example.com/steal", origin)).toBe("/");
    expect(resolveChunkRetryPath("https://example.com/steal", origin)).toBe("/");
  });
});
