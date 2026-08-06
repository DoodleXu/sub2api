import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount } from '@vue/test-utils'
import AliyunCaptchaWidget from '../AliyunCaptchaWidget.vue'

interface CapturedInitOptions {
  SceneId: string
  prefix: string
  mode: string
  element: string
  button: string
  captchaVerifyCallback: (param: string) => { captchaResult: boolean }
  language?: string
}

const i18nStub = {
  install(app: { config: { globalProperties: Record<string, unknown> } }) {
    app.config.globalProperties.$t = (key: string) => key
  }
}

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ locale: { value: 'zh-CN' }, t: (key: string) => key })
}))

describe('AliyunCaptchaWidget', () => {
  let initOptions: CapturedInitOptions | null

  beforeEach(() => {
    initOptions = null
    window.initAliyunCaptcha = vi.fn((options: CapturedInitOptions) => {
      initOptions = options
    }) as unknown as typeof window.initAliyunCaptcha
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
    delete window.initAliyunCaptcha
    delete window.AliyunCaptchaConfig
    document.getElementById('aliyunCaptcha-window-popup')?.remove()
    document.getElementById('aliyunCaptcha-mask')?.remove()
    document
      .querySelectorAll('script[src*="aliyunCaptcha/AliyunCaptcha"]')
      .forEach((script) => script.remove())
  })

  function mountWidget() {
    return mount(AliyunCaptchaWidget, {
      props: { sceneId: 'scene-1', prefix: 'prefix-1', region: 'cn' as const },
      attachTo: document.body,
      global: { plugins: [i18nStub] }
    })
  }

  function createVisiblePopup(): HTMLElement {
    const popup = document.createElement('div')
    popup.id = 'aliyunCaptcha-window-popup'
    popup.style.display = 'block'
    document.body.appendChild(popup)
    return popup
  }

  function flushAsync(): Promise<void> {
    return new Promise((resolve) => window.setTimeout(resolve, 0))
  }

  it('渲染可见验证按钮并以 popup 模式初始化，全局配置就位', async () => {
    const wrapper = mountWidget()
    await Promise.resolve()
    await Promise.resolve()

    const button = wrapper.get('button')
    expect(button.text()).toContain('auth.captchaClickToVerify')
    expect(window.AliyunCaptchaConfig).toEqual({ region: 'cn', prefix: 'prefix-1' })
    expect(initOptions).not.toBeNull()
    expect(initOptions!.mode).toBe('popup')
    expect(initOptions!.SceneId).toBe('scene-1')
    expect(initOptions!.language).toBe('cn')

    wrapper.unmount()
  })

  it('脚本加载失败后再次 verify() 会重建 script 并完成验证', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    delete window.initAliyunCaptcha
    const wrapper = mountWidget()
    await Promise.resolve()

    const firstScript = document.querySelector<HTMLScriptElement>(
      'script[src*="aliyunCaptcha/AliyunCaptcha"]'
    )
    expect(firstScript).not.toBeNull()
    firstScript!.dispatchEvent(new Event('error'))
    await flushAsync()

    expect(document.querySelector('script[src*="aliyunCaptcha/AliyunCaptcha"]')).toBeNull()

    const vm = wrapper.vm as unknown as { verify: () => Promise<string | null> }
    const verification = vm.verify()
    await Promise.resolve()

    const secondScript = document.querySelector<HTMLScriptElement>(
      'script[src*="aliyunCaptcha/AliyunCaptcha"]'
    )
    expect(secondScript).not.toBeNull()
    expect(secondScript).not.toBe(firstScript)
    window.initAliyunCaptcha = vi.fn((options: CapturedInitOptions) => {
      initOptions = options
    }) as unknown as typeof window.initAliyunCaptcha
    secondScript!.dispatchEvent(new Event('load'))
    await flushAsync()
    expect(wrapper.get('button').text()).toContain('auth.captchaVerifying')

    initOptions!.captchaVerifyCallback('captcha-param-retry')
    await expect(verification).resolves.toBe('captcha-param-retry')

    wrapper.unmount()
  })

  it('脚本 load 后初始化函数尚未就绪时会清理并允许再次验证', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    delete window.initAliyunCaptcha
    const wrapper = mountWidget()
    await Promise.resolve()

    const firstScript = document.querySelector<HTMLScriptElement>(
      'script[src*="aliyunCaptcha/AliyunCaptcha"]'
    )
    expect(firstScript).not.toBeNull()
    firstScript!.dispatchEvent(new Event('load'))
    await flushAsync()

    expect(document.querySelector('script[src*="aliyunCaptcha/AliyunCaptcha"]')).toBeNull()

    const vm = wrapper.vm as unknown as { verify: () => Promise<string | null> }
    const verification = vm.verify()
    await Promise.resolve()

    const secondScript = document.querySelector<HTMLScriptElement>(
      'script[src*="aliyunCaptcha/AliyunCaptcha"]'
    )
    expect(secondScript).not.toBeNull()
    expect(secondScript).not.toBe(firstScript)
    window.initAliyunCaptcha = vi.fn((options: CapturedInitOptions) => {
      initOptions = options
    }) as unknown as typeof window.initAliyunCaptcha
    secondScript!.dispatchEvent(new Event('load'))
    await flushAsync()

    initOptions!.captchaVerifyCallback('captcha-param-loaded-retry')
    await expect(verification).resolves.toBe('captcha-param-loaded-retry')

    wrapper.unmount()
  })

  it('用户点击按钮进入验证中，验证完成后 emit verify 并置已通过', async () => {
    const wrapper = mountWidget()
    await Promise.resolve()
    await Promise.resolve()

    await wrapper.get('button').trigger('click')
    expect(wrapper.get('button').text()).toContain('auth.captchaVerifying')

    const result = initOptions!.captchaVerifyCallback('captcha-param-1')
    expect(result).toEqual({ captchaResult: true })
    await wrapper.vm.$nextTick()

    expect(wrapper.emitted('verify')).toEqual([['captcha-param-1']])
    expect(wrapper.get('button').text()).toContain('auth.captchaVerified')
    expect(wrapper.get('button').attributes('disabled')).toBeDefined()

    wrapper.unmount()
  })

  it('verify() 在已预验证时直接复用缓存的 captchaVerifyParam', async () => {
    const wrapper = mountWidget()
    await Promise.resolve()
    await Promise.resolve()

    await wrapper.get('button').trigger('click')
    initOptions!.captchaVerifyCallback('captcha-param-2')

    const vm = wrapper.vm as unknown as { verify: () => Promise<string | null> }
    await expect(vm.verify()).resolves.toBe('captcha-param-2')

    wrapper.unmount()
  })

  it('verify() 在未预验证时触发弹窗流程并等待结果', async () => {
    const wrapper = mountWidget()
    await Promise.resolve()
    await Promise.resolve()

    const vm = wrapper.vm as unknown as { verify: () => Promise<string | null> }
    const pending = vm.verify()
    await Promise.resolve()
    await Promise.resolve()
    await wrapper.vm.$nextTick()
    expect(wrapper.get('button').text()).toContain('auth.captchaVerifying')

    initOptions!.captchaVerifyCallback('captcha-param-3')
    await expect(pending).resolves.toBe('captcha-param-3')

    wrapper.unmount()
  })

  it('弹窗未出现前会按 tick 重试触发按钮（SDK 异步绑定兜底）', async () => {
    vi.useFakeTimers()
    const wrapper = mountWidget()
    await Promise.resolve()
    await Promise.resolve()

    const vm = wrapper.vm as unknown as { verify: () => Promise<string | null> }
    void vm.verify()
    await Promise.resolve()
    await Promise.resolve()

    const button = wrapper.get('button').element as HTMLButtonElement
    const clickSpy = vi.fn()
    button.addEventListener('click', clickSpy)

    await vi.advanceTimersByTimeAsync(1000)
    expect(clickSpy.mock.calls.length).toBeGreaterThanOrEqual(3)

    button.removeEventListener('click', clickSpy)
    wrapper.unmount()
  })

  it('弹窗出现后被用户关闭时 resolve null 并回到未验证态', async () => {
    vi.useFakeTimers()
    const wrapper = mountWidget()
    await Promise.resolve()
    await Promise.resolve()

    const vm = wrapper.vm as unknown as { verify: () => Promise<string | null> }
    const pending = vm.verify()
    await Promise.resolve()
    await Promise.resolve()

    const popup = createVisiblePopup()
    await vi.advanceTimersByTimeAsync(400)
    popup.remove()
    await vi.advanceTimersByTimeAsync(400)

    await expect(pending).resolves.toBeNull()
    expect(wrapper.get('button').text()).toContain('auth.captchaClickToVerify')

    wrapper.unmount()
  })

  it('reset() 清空缓存并取消进行中的验证', async () => {
    const wrapper = mountWidget()
    await Promise.resolve()
    await Promise.resolve()

    await wrapper.get('button').trigger('click')
    initOptions!.captchaVerifyCallback('captcha-param-4')

    const vm = wrapper.vm as unknown as {
      verify: () => Promise<string | null>
      reset: () => void
    }
    vm.reset()
    await wrapper.vm.$nextTick()
    expect(wrapper.get('button').text()).toContain('auth.captchaClickToVerify')

    // reset 后缓存失效，verify() 重新走弹窗流程
    const pending = vm.verify()
    await Promise.resolve()
    initOptions!.captchaVerifyCallback('captcha-param-5')
    await expect(pending).resolves.toBe('captcha-param-5')

    wrapper.unmount()
  })
})

declare global {
  interface Window {
    initAliyunCaptcha?: (options: unknown) => void
    AliyunCaptchaConfig?: { region: string; prefix: string }
  }
}
