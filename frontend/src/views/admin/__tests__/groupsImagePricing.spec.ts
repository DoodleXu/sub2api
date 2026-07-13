import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

import {
  getDefaultImagePreviewPrice,
  getDefaultVideoPreviewPrice,
  getImagePricePlaceholder,
  getVideoPricePlaceholder,
  imagePricingPlatforms,
  imagePricingI18nKey,
  supportsImagePricingPlatform,
  supportsVideoPricingPlatform,
  videoPricingI18nKey,
} from "../groupsImagePricing";

const currentDir = dirname(fileURLToPath(import.meta.url));
const groupsViewSource = readFileSync(resolve(currentDir, "../GroupsView.vue"), "utf8");

describe("groups image pricing platform support", () => {
  it("includes Grok image groups", () => {
    expect(supportsImagePricingPlatform("grok")).toBe(true);
    expect(imagePricingPlatforms.has("grok")).toBe(true);
  });

  it("enables video pricing controls for Grok only", () => {
    expect(supportsVideoPricingPlatform("grok")).toBe(true);
    expect(supportsVideoPricingPlatform("openai")).toBe(false);
  });

  it("keeps non-media group platforms out of the image pricing controls", () => {
    expect(supportsImagePricingPlatform("anthropic")).toBe(false);
  });

  it("keeps image and video pricing copy separate", () => {
    expect(imagePricingI18nKey("grok", "title")).toBe(
      "admin.groups.imagePricing.title",
    );
    expect(videoPricingI18nKey("title")).toBe("admin.groups.videoPricing.title");
  });

  it("uses Grok media defaults instead of generic image fallback placeholders", () => {
    expect(getImagePricePlaceholder("grok", "image_price_1k")).toBe("0.02");
    expect(getImagePricePlaceholder("grok", "image_price_2k")).toBe("0.02");
    // 视频 placeholder 为每秒单价：480p/720p 取 grok-imagine-video 官方每秒价，
    // 1080p 仅 video-1.5 支持、取 1.5 每秒价。
    expect(getVideoPricePlaceholder("grok", "video_price_480p")).toBe("0.05");
    expect(getVideoPricePlaceholder("grok", "video_price_720p")).toBe("0.07");
    expect(getVideoPricePlaceholder("grok", "video_price_1080p")).toBe("0.25");
  });

  it("keeps non-Grok image placeholders on the generic image card", () => {
    expect(getImagePricePlaceholder("openai", "image_price_1k")).toBe("0.134");
    expect(getDefaultImagePreviewPrice("openai", "image_price_2k")).toBe(0.201);
    expect(getDefaultVideoPreviewPrice("openai", "video_price_480p")).toBeNull();
  });

  it("uses platform-aware image placeholders in both group forms", () => {
    expect(groupsViewSource).toContain(
      ":placeholder=\"getImagePricePlaceholder(createForm.platform, 'image_price_1k')\"",
    );
    expect(groupsViewSource).toContain(
      ":placeholder=\"getImagePricePlaceholder(editForm.platform, 'image_price_1k')\"",
    );
  });

  it("renders batch image controls in both group forms", () => {
    expect(groupsViewSource.match(/v-model=\"createForm\.allow_batch_image_generation\"/g)).toHaveLength(1);
    expect(groupsViewSource.match(/v-model=\"editForm\.allow_batch_image_generation\"/g)).toHaveLength(1);
    expect(groupsViewSource).toContain("resetDisabledBatchImagePricing(createForm)");
    expect(groupsViewSource).toContain("resetDisabledBatchImagePricing(editForm)");
  });
});
