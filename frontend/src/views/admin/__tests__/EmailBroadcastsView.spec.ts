import { beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent, h } from "vue";
import { flushPromises, mount } from "@vue/test-utils";

import EmailBroadcastsView from "../EmailBroadcastsView.vue";

const {
  getEmailBroadcastDraft,
  deleteEmailBroadcastDraft,
  listEmailBroadcasts,
  sendEmailBroadcast,
  saveEmailBroadcastDraft,
  cancelEmailBroadcast,
  resumeEmailBroadcast,
  preflightEmailBroadcast,
  previewEmailBroadcastRecipients,
  listEmailBroadcastRecipients,
  showError,
  showSuccess,
  stepUpRun,
} = vi.hoisted(() => ({
  getEmailBroadcastDraft: vi.fn(),
  deleteEmailBroadcastDraft: vi.fn(),
  listEmailBroadcasts: vi.fn(),
  sendEmailBroadcast: vi.fn(),
  saveEmailBroadcastDraft: vi.fn(),
  cancelEmailBroadcast: vi.fn(),
  resumeEmailBroadcast: vi.fn(),
  preflightEmailBroadcast: vi.fn(),
  previewEmailBroadcastRecipients: vi.fn(),
  listEmailBroadcastRecipients: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
  stepUpRun: vi.fn((action: () => Promise<unknown>) => action()),
}));

vi.mock("@/api/admin", () => ({
  adminAPI: {
    settings: {
      getEmailBroadcastDraft,
      deleteEmailBroadcastDraft,
      listEmailBroadcasts,
      sendEmailBroadcast,
      saveEmailBroadcastDraft,
      cancelEmailBroadcast,
      resumeEmailBroadcast,
      preflightEmailBroadcast,
      previewEmailBroadcastRecipients,
      listEmailBroadcastRecipients,
    },
  },
}));

vi.mock("@/stores", () => ({
  useAppStore: () => ({
    showError,
    showSuccess,
  }),
}));

vi.mock("@/utils/apiError", () => ({
  extractApiErrorMessage: (_error: unknown, fallback: string) => fallback,
}));

vi.mock("@/composables/useStepUp", () => ({
  useStepUp: () => ({ run: stepUpRun }),
  isStepUpCancelled: () => false,
  isStepUpBlocked: () => false,
  stepUpBlockReason: () => "",
}));

vi.mock("vue-i18n", async () => {
  const actual = await vi.importActual<typeof import("vue-i18n")>("vue-i18n");
  const translations: Record<string, string> = {
    "admin.settings.emailBroadcast.title": "邮件群发",
    "admin.settings.emailBroadcast.pageDescription": "发送邮件通知。",
    "admin.settings.emailBroadcast.refreshTasks": "刷新任务",
    "admin.settings.emailBroadcast.composeTitle": "撰写邮件",
    "admin.settings.emailBroadcast.description": "编辑要发送的邮件。",
    "admin.settings.emailBroadcast.rateHint": "约每 {seconds} 秒一封",
    "admin.settings.emailBroadcast.draftSavedAt": "草稿已保存于 {time}",
    "admin.settings.emailBroadcast.noDraft": "暂无草稿",
    "admin.settings.emailBroadcast.loadDraft": "加载草稿",
    "admin.settings.emailBroadcast.saveDraft": "保存草稿",
    "admin.settings.emailBroadcast.clearDraft": "清空草稿",
    "admin.settings.emailBroadcast.scope": "范围",
    "admin.settings.emailBroadcast.minBalanceEnabled": "按余额筛选",
    "admin.settings.emailBroadcast.minBalanceLabel": "余额大于",
    "admin.settings.emailBroadcast.minBalanceInvalid": "请输入有效的非负余额门槛。",
    "admin.settings.emailBroadcast.minBalanceSummary": "余额大于 {amount}。",
    "admin.settings.emailBroadcast.confirmPreflightSummary": "发送范围：{scope}；目标 {target} 人。",
    "admin.settings.emailBroadcast.locale": "语言",
    "admin.settings.emailBroadcast.rpm": "每分钟发送数",
    "admin.settings.emailBroadcast.customUserIds": "用户 ID",
    "admin.settings.emailBroadcast.customUserIdsPlaceholder": "每行一个用户 ID",
    "admin.settings.emailBroadcast.customEmails": "邮箱",
    "admin.settings.emailBroadcast.customEmailsPlaceholder": "每行一个邮箱",
    "admin.settings.emailBroadcast.messageTitle": "标题",
    "admin.settings.emailBroadcast.messageTitlePlaceholder": "邮件标题",
    "admin.settings.emailBroadcast.messageHtml": "正文 HTML",
    "admin.settings.emailBroadcast.messageHtmlPlaceholder": "邮件正文",
    "admin.settings.emailBroadcast.actionLabel": "按钮文案",
    "admin.settings.emailBroadcast.actionLabelPlaceholder": "立即查看",
    "admin.settings.emailBroadcast.actionUrl": "按钮链接",
    "admin.settings.emailBroadcast.send": "发送",
    "admin.settings.emailBroadcast.sending": "发送中",
    "admin.settings.emailBroadcast.taskTitle": "发送任务",
    "admin.settings.emailBroadcast.taskDescription": "查看任务进度。",
    "admin.settings.emailBroadcast.noTasks": "暂无任务",
    "admin.settings.emailBroadcast.scopes.activeUsers": "活跃用户",
    "admin.settings.emailBroadcast.scopes.allUsers": "全部用户",
    "admin.settings.emailBroadcast.scopes.admins": "管理员",
    "admin.settings.emailBroadcast.scopes.custom": "自定义",
    "admin.settings.emailBroadcast.locales.zh": "中文",
    "admin.settings.emailBroadcast.locales.en": "英文",
    "admin.settings.emailBroadcast.draftCleared": "草稿已清空",
    "admin.settings.emailBroadcast.confirmTitle": "确认发送",
    "admin.settings.emailBroadcast.confirmSend": "确认发送",
    "admin.settings.emailBroadcast.details": "明细",
    "admin.settings.emailBroadcast.recipientDetails": "收件人明细（共 {count} 条）",
    "common.actions": "操作",
  };

  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, string | number>) =>
        (translations[key] ?? key).replace(
          /\{(\w+)\}/g,
          (_, token) => String(params?.[token] ?? `{${token}}`),
        ),
    }),
  };
});

const AppLayoutStub = { template: "<div><slot /></div>" };

const SelectStub = defineComponent({
  props: {
    modelValue: {
      type: [String, Number, Boolean, null],
      default: "",
    },
    options: {
      type: Array,
      default: () => [],
    },
  },
  emits: ["update:modelValue"],
  setup(props, { emit }) {
    return () =>
      h(
        "select",
        {
          value: props.modelValue ?? "",
          onChange: (event: Event) => {
            emit("update:modelValue", (event.target as HTMLSelectElement).value);
          },
        },
        (props.options as Array<Record<string, unknown>>).map((option) =>
          h(
            "option",
            {
              key: String(option.value),
              value: option.value as string,
            },
            String(option.label ?? ""),
          ),
        ),
      );
  },
});

const ConfirmDialogStub = {
  props: ["show"],
  template: '<div v-if="show" data-testid="confirm-dialog"><slot /></div>',
};

const PaginationStub = defineComponent({
  props: ["page", "total", "pageSize"],
  emits: ["update:page", "update:pageSize"],
  setup(_props, { emit }) {
    return () =>
      h("div", { "data-testid": "recipient-pagination" }, [
        h("button", { "data-testid": "recipient-next", onClick: () => emit("update:page", 2) }, "next"),
        h("button", { "data-testid": "recipient-page-size", onClick: () => emit("update:pageSize", 50) }, "size"),
      ]);
  },
});

function createDeferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });

  return { promise, resolve, reject };
}

function mountView() {
  return mount(EmailBroadcastsView, {
    global: {
      stubs: {
        AppLayout: AppLayoutStub,
        Select: SelectStub,
        ConfirmDialog: ConfirmDialogStub,
        Pagination: PaginationStub,
        Icon: true,
        TotpStepUpDialog: true,
      },
    },
  });
}

describe("EmailBroadcastsView", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    listEmailBroadcasts.mockResolvedValue({ jobs: [] });
    getEmailBroadcastDraft.mockResolvedValue({
      scope: "custom",
      locale: "en",
      message_title: "旧草稿标题",
      message_html: "<p>旧草稿正文</p>",
      action_label: "Open",
      action_url: "/old",
      user_ids: [1001, 1002],
      emails: ["first@example.com", "second@example.com"],
      rpm: 12,
      saved_at: "2026-06-16T08:00:00Z",
    });
    deleteEmailBroadcastDraft.mockResolvedValue({ deleted: true });
    sendEmailBroadcast.mockResolvedValue({
      batch_id: "batch-1",
      target_count: 1,
      rpm: 6,
      estimated_duration_seconds: 10,
      started_at: "2026-06-16T08:01:00Z",
    });
    saveEmailBroadcastDraft.mockImplementation(async (request) => ({ ...request, saved_at: "2026-06-16T08:00:00Z" }));
    cancelEmailBroadcast.mockResolvedValue({});
    resumeEmailBroadcast.mockResolvedValue({});
    preflightEmailBroadcast.mockResolvedValue({
      target_count: 1,
      valid_count: 1,
      invalid_count: 0,
      unsubscribed_count: 0,
      estimated_duration_seconds: 0,
      sample_emails: [],
      domains: {},
    });
    previewEmailBroadcastRecipients.mockResolvedValue({
      target_count: 1,
      valid_count: 1,
      invalid_count: 0,
      unsubscribed_count: 0,
      estimated_duration_seconds: 0,
      sample_emails: [],
      domains: {},
    });
    listEmailBroadcastRecipients.mockResolvedValue({
      recipients: [
        {
          email: "first@example.com",
          normalized_email: "first@example.com",
          locale: "en",
          status: "sent",
          attempt_count: 1,
          message_id: "message-1",
          updated_at: "2026-06-16T08:01:00Z",
        },
      ],
      total: 250,
    });
  });

  it("resets the compose form, custom inputs, saved hint and button states after clearing a draft", async () => {
    const wrapper = mountView();

    await flushPromises();

    const clearDraftButton = () =>
      wrapper
        .findAll("button")
        .find((button) => button.text().includes("清空草稿"));
    const sendButton = () =>
      wrapper.findAll("button").find((button) => button.text().includes("发送"));

    expect(wrapper.text()).toContain("草稿已保存于");
    expect(clearDraftButton()?.attributes("disabled")).toBeUndefined();
    expect(sendButton()?.attributes("disabled")).toBeUndefined();
    expect(wrapper.find('input[placeholder="邮件标题"]').element).toHaveProperty(
      "value",
      "旧草稿标题",
    );
    expect(wrapper.find('textarea[placeholder="邮件正文"]').element).toHaveProperty(
      "value",
      "<p>旧草稿正文</p>",
    );
    expect(wrapper.find('textarea[placeholder="每行一个用户 ID"]').element).toHaveProperty(
      "value",
      "1001\n1002",
    );
    expect(wrapper.find('textarea[placeholder="每行一个邮箱"]').element).toHaveProperty(
      "value",
      "first@example.com\nsecond@example.com",
    );

    await clearDraftButton()?.trigger("click");
    await flushPromises();

    expect(deleteEmailBroadcastDraft).toHaveBeenCalledTimes(1);
    expect(showSuccess).toHaveBeenCalledWith("草稿已清空");
    expect(wrapper.text()).toContain("暂无草稿");
    expect(clearDraftButton()?.attributes("disabled")).toBeDefined();
    expect(sendButton()?.attributes("disabled")).toBeDefined();
    expect(wrapper.find('input[placeholder="邮件标题"]').element).toHaveProperty("value", "");
    expect(wrapper.find('textarea[placeholder="邮件正文"]').element).toHaveProperty("value", "");
    expect(wrapper.find('input[placeholder="立即查看"]').element).toHaveProperty("value", "");
    expect(wrapper.find('input[placeholder="/notice"]').element).toHaveProperty("value", "");

    const selects = wrapper.findAll("select");
    expect((selects[0].element as HTMLSelectElement).value).toBe("active_users");
    expect((selects[1].element as HTMLSelectElement).value).toBe("zh");
    expect((wrapper.find('input[type="number"]').element as HTMLInputElement).value).toBe("6");

    await selects[0].setValue("custom");
    await flushPromises();

    expect(wrapper.find('textarea[placeholder="每行一个用户 ID"]').element).toHaveProperty(
      "value",
      "",
    );
    expect(wrapper.find('textarea[placeholder="每行一个邮箱"]').element).toHaveProperty(
      "value",
      "",
    );
  });

  it("keeps send disabled while a draft clear request is pending", async () => {
    const clearRequest = createDeferred<{ deleted: boolean }>();
    deleteEmailBroadcastDraft.mockReturnValueOnce(clearRequest.promise);
    const wrapper = mountView();

    await flushPromises();

    const clearDraftButton = () =>
      wrapper
        .findAll("button")
        .find((button) => button.text().includes("清空草稿"));
    const sendButton = () =>
      wrapper.findAll("button").find((button) => button.text().includes("发送"));

    expect(sendButton()?.attributes("disabled")).toBeUndefined();

    await clearDraftButton()?.trigger("click");
    await wrapper.vm.$nextTick();

    expect(deleteEmailBroadcastDraft).toHaveBeenCalledTimes(1);
    expect(sendButton()?.attributes("disabled")).toBeDefined();

    await sendButton()?.trigger("click");
    await wrapper.vm.$nextTick();

    expect(wrapper.find('[data-testid="confirm-dialog"]').exists()).toBe(false);
    expect(sendEmailBroadcast).not.toHaveBeenCalled();

    clearRequest.resolve({ deleted: true });
    await flushPromises();

    expect(wrapper.text()).toContain("暂无草稿");
    expect(sendButton()?.attributes("disabled")).toBeDefined();
  });

  it("loads every recipient page instead of exposing only the first 100 rows", async () => {
    listEmailBroadcasts.mockResolvedValueOnce({
      jobs: [
        {
          batch_id: "batch-many",
          status: "completed",
          scope: "all_users",
          locale: "auto",
          message_title: "Notice",
          target_count: 250,
          sent_count: 250,
          skipped_count: 0,
          unsubscribed_count: 0,
          failure_count: 0,
          uncertain_count: 0,
          rpm: 30,
          started_at: "2026-06-16T08:00:00Z",
          updated_at: "2026-06-16T08:10:00Z",
        },
      ],
    });
    const wrapper = mountView();
    await flushPromises();

    const details = wrapper.findAll("button").find((button) => button.text().includes("明细"));
    await details?.trigger("click");
    await flushPromises();

    expect(listEmailBroadcastRecipients).toHaveBeenLastCalledWith("batch-many", "", 1, 100);
    expect(wrapper.text()).toContain("收件人明细（共 250 条）");

    await wrapper.get('[data-testid="recipient-next"]').trigger("click");
    await flushPromises();
    expect(listEmailBroadcastRecipients).toHaveBeenLastCalledWith("batch-many", "", 2, 100);

    await wrapper.get('[data-testid="recipient-page-size"]').trigger("click");
    await flushPromises();
    expect(listEmailBroadcastRecipients).toHaveBeenLastCalledWith("batch-many", "", 1, 50);
  });

  it("sends a zero balance threshold through preflight and saves it in drafts", async () => {
    getEmailBroadcastDraft.mockResolvedValueOnce(null);
    const wrapper = mountView();
    await flushPromises();
    await wrapper.get('input[placeholder="邮件标题"]').setValue("Notice");
    await wrapper.get('textarea[placeholder="邮件正文"]').setValue("<p>Notice</p>");
    await wrapper.get('input[type="checkbox"]').setValue(true);
    await wrapper.get('input[aria-label="余额大于"]').setValue("0");
    const saveButton = wrapper.findAll("button").find((button) => button.text().includes("保存草稿"));
    await saveButton?.trigger("click");
    await flushPromises();
    expect(saveEmailBroadcastDraft).toHaveBeenCalledWith(expect.objectContaining({ min_balance_exclusive: 0 }));
    const sendButton = wrapper.findAll("button").find((button) => button.text().includes("发送"));
    await sendButton?.trigger("click");
    await flushPromises();
    expect(preflightEmailBroadcast).toHaveBeenCalledWith(expect.objectContaining({ min_balance_exclusive: 0 }));
    expect(wrapper.get('[data-testid="confirm-dialog"]').text()).toContain("余额大于 0");
    expect(wrapper.get('[data-testid="confirm-dialog"]').text()).toContain("目标 1 人");
  });

  it("does not require step-up for candidate preview but does for the send flow", async () => {
    vi.useFakeTimers();
    getEmailBroadcastDraft.mockResolvedValueOnce(null);
    const wrapper = mountView();
    await flushPromises();
    await vi.advanceTimersByTimeAsync(300);
    await flushPromises();

    expect(previewEmailBroadcastRecipients).toHaveBeenCalled();
    expect(stepUpRun).not.toHaveBeenCalled();

    await wrapper.get('input[placeholder="邮件标题"]').setValue("Notice");
    await wrapper.get('textarea[placeholder="邮件正文"]').setValue("<p>Notice</p>");
    const sendButton = wrapper.findAll("button").find((button) => button.text().includes("发送"));
    await sendButton?.trigger("click");
    await flushPromises();

    expect(stepUpRun).toHaveBeenCalledTimes(1);
    expect(preflightEmailBroadcast).toHaveBeenCalledTimes(1);
    vi.useRealTimers();
  });

  it("restores a balance threshold from a draft and omits it for custom recipients", async () => {
    getEmailBroadcastDraft.mockResolvedValueOnce({
      scope: "all_users", locale: "zh", message_title: "Notice", message_html: "<p>Notice</p>",
      rpm: 6, min_balance_exclusive: 5, saved_at: "2026-06-16T08:00:00Z",
    });
    const wrapper = mountView();
    await flushPromises();
    expect((wrapper.get('input[aria-label="余额大于"]').element as HTMLInputElement).value).toBe("5");
    const clearDraftButton = wrapper.findAll("button").find((button) => button.text().includes("清空草稿"));
    await clearDraftButton?.trigger("click");
    await flushPromises();
    expect(wrapper.find('input[aria-label="余额大于"]').exists()).toBe(false);
    await wrapper.get('input[type="checkbox"]').setValue(true);
    await wrapper.get('input[aria-label="余额大于"]').setValue("5");
    await wrapper.findAll("select")[0].setValue("custom");
    expect(wrapper.find('input[aria-label="余额大于"]').exists()).toBe(false);
    await wrapper.get('textarea[placeholder="每行一个邮箱"]').setValue("user@example.com");
    const saveButton = wrapper.findAll("button").find((button) => button.text().includes("保存草稿"));
    await saveButton?.trigger("click");
    expect(saveEmailBroadcastDraft).toHaveBeenCalledWith(expect.objectContaining({ scope: "custom", min_balance_exclusive: undefined }));
  });

  it("rejects an enabled balance filter without an amount", async () => {
    getEmailBroadcastDraft.mockResolvedValueOnce(null);
    const wrapper = mountView();
    await flushPromises();
    await wrapper.get('input[type="checkbox"]').setValue(true);
    const saveButton = wrapper.findAll("button").find((button) => button.text().includes("保存草稿"));
    await saveButton?.trigger("click");
    expect(saveEmailBroadcastDraft).not.toHaveBeenCalled();
    expect(showError).toHaveBeenCalledWith("请输入有效的非负余额门槛。");
  });
});
