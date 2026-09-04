import { describe, expect, it } from "vitest";

import {
  InvalidSystemSettingsResponseError,
  normalizeCustomEndpoints,
  normalizeSystemSettingsResponse,
} from "@/api/admin/settings";

function validSettingsPayload(overrides: Record<string, unknown> = {}) {
  return {
    registration_enabled: true,
    email_verify_enabled: true,
    frontend_url: "https://example.com",
    default_balance: 0,
    default_concurrency: 5,
    site_name: "Sub2API",
    api_base_url: "https://api.example.com",
    table_default_page_size: 20,
    backend_mode_enabled: false,
    smtp_host: "",
    payment_enabled: false,
    payment_min_amount: 1,
    channel_monitor_enabled: true,
    available_channels_enabled: true,
    web_console_enabled: true,
    ...overrides,
  };
}

describe("admin settings response normalization", () => {
  it("replaces null or missing array fields with safe defaults", () => {
    const settings = normalizeSystemSettingsResponse(validSettingsPayload({
      web_console_enabled: true,
      custom_endpoints: null,
      payment_enabled_types: null,
      table_page_size_options: [10, 50],
      login_agreement_documents: null,
    }));

    expect(settings.custom_endpoints).toEqual([]);
    expect(settings.payment_enabled_types).toEqual([]);
    expect(settings.passkey_rp_origins).toEqual([]);
    expect(settings.table_page_size_options).toEqual([10, 50]);
    expect(settings.login_agreement_documents).toEqual([]);
  });

  it("preserves valid custom endpoint records", () => {
    expect(
      normalizeCustomEndpoints([
        {
          endpoint: "https://api.example.com",
          name: "Primary",
          description: "Primary endpoint",
        },
        {
          endpoint: "https://backup.example.com",
          name: "Backup",
          description: "Secondary endpoint",
        },
      ]),
    ).toEqual([
      {
        endpoint: "https://api.example.com",
        name: "Primary",
        description: "Primary endpoint",
      },
      {
        endpoint: "https://backup.example.com",
        name: "Backup",
        description: "Secondary endpoint",
      },
    ]);
  });

  it.each([
    ["payment_enabled_types", { alipay: true }],
    ["passkey_rp_origins", "https://example.com"],
    ["table_page_size_options", [10, "20", Number.NaN, 50]],
    ["login_agreement_documents", [null, { id: "terms" }]],
    ["custom_endpoints", [{ endpoint: "https://api.example.com" }]],
    ["account_quota_notify_emails", [{}]],
    ["payment_recharge_gift_tiers", [{ threshold: 10, percent: "5" }]],
  ])("rejects malformed %s values instead of making them writable", (field, malformed) => {
    expect(() =>
      normalizeSystemSettingsResponse(validSettingsPayload({ [field]: malformed })),
    ).toThrowError(
      expect.objectContaining<Partial<InvalidSystemSettingsResponseError>>({
        name: "InvalidSystemSettingsResponseError",
        field,
      }),
    );
  });

  it("rejects a non-object settings payload", () => {
    expect(() => normalizeSystemSettingsResponse(null)).toThrow(
      "Invalid system settings response",
    );
  });

  it.each([
    ["empty object", {}],
    ["partial object", { registration_enabled: true }],
  ])("rejects an incomplete %s payload", (_label, payload) => {
    expect(() => normalizeSystemSettingsResponse(payload)).toThrowError(
      expect.objectContaining({
        name: "InvalidSystemSettingsResponseError",
      }),
    );
  });

  it("rejects a required scalar with the wrong type", () => {
    expect(() =>
      normalizeSystemSettingsResponse(
        validSettingsPayload({ payment_enabled: "false" }),
      ),
    ).toThrowError(
      expect.objectContaining({
        name: "InvalidSystemSettingsResponseError",
        field: "payment_enabled",
      }),
    );
  });
});
