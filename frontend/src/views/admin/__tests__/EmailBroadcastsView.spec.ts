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
  showError,
  showSuccess,
} = vi.hoisted(() => ({
  getEmailBroadcastDraft: vi.fn(),
  deleteEmailBroadcastDraft: vi.fn(),
  listEmailBroadcasts: vi.fn(),
  sendEmailBroadcast: vi.fn(),
  saveEmailBroadcastDraft: vi.fn(),
  cancelEmailBroadcast: vi.fn(),
  resumeEmailBroadcast: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
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
        Icon: true,
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
    saveEmailBroadcastDraft.mockResolvedValue({});
    cancelEmailBroadcast.mockResolvedValue({});
    resumeEmailBroadcast.mockResolvedValue({});
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
});
