import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import EmailBroadcastTemplatePanel from '../EmailBroadcastTemplatePanel.vue'
import { rechargeBonusTemplate, getWechatQrUrl, replaceWechatQrUrl } from '@/utils/emailBroadcastTemplates'
import { createEmailPreviewDocument } from '@/utils/emailPreview'
import type { EmailBroadcastContent } from '@/api/admin/settings'

const api = vi.hoisted(() => ({
  listEmailBroadcastTemplates: vi.fn(), saveEmailBroadcastTemplate: vi.fn(),
  deleteEmailBroadcastTemplate: vi.fn(), previewEmailBroadcastContent: vi.fn(),
}))
vi.mock('@/api/admin', () => ({ adminAPI: { settings: api } }))
vi.mock('@/stores', () => ({ useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn() }) }))
vi.mock('@/composables/useStepUp', () => ({ useStepUp: () => ({ run: (action: () => Promise<unknown>) => action() }), isStepUpCancelled: () => false, isStepUpBlocked: () => false }))
vi.mock('vue-i18n', async (importOriginal) => ({ ...await importOriginal<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key }) }))

function mountPanel() {
  return mount(EmailBroadcastTemplatePanel, {
    props: { content: { locale: 'zh', message_title: '活动', message_html: '<p>正文</p>', action_label: '', action_url: '' } },
    global: { stubs: { ConfirmDialog: true, TotpStepUpDialog: true } },
  })
}
describe('Email broadcast content templates', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.listEmailBroadcastTemplates.mockResolvedValue([])
    api.previewEmailBroadcastContent.mockResolvedValue({ subject: '活动', html: '<html><head></head><body>正文</body></html>' })
  })
  it('applies the six-tier built-in content without recipient or rate fields', async () => {
    const panel = mountPanel()
    await flushPromises()
    await panel.get('select').setValue(rechargeBonusTemplate.id)
    await panel.findAll('button')[0]!.trigger('click')
    const content = panel.emitted('apply')![0]![0] as Record<string, unknown>
    expect(Object.keys(content).sort()).toEqual(['action_label', 'action_url', 'locale', 'message_html', 'message_title'])
    expect(content.message_html).toContain('满 30 元')
    expect(content.message_html).toContain('赠送 3%')
    expect(content.message_html).toContain('赠送 10%')
    panel.unmount()
  })
  it('persists current content as a named custom template', async () => {
    const panel = mountPanel()
    api.saveEmailBroadcastTemplate.mockResolvedValue({ ...rechargeBonusTemplate, id: 'custom', name: '通知' })
    await flushPromises()
    await panel.get('#broadcast-template-name').setValue('通知')
    await panel.findAll('button')[2]!.trigger('click')
    await flushPromises()
    expect(api.saveEmailBroadcastTemplate).toHaveBeenCalledWith({ ...panel.props('content'), name: '通知' }, undefined)
    expect(panel.get('select').element.value).toBe('custom')
    panel.unmount()
  })
  it('keeps the QR editable after loading saved content and updates both image and link', async () => {
    const panel = mountPanel()
    await panel.setProps({ content: { ...panel.props('content'), message_html: rechargeBonusTemplate.message_html } })
    await panel.get('#broadcast-wechat-qr').setValue('https://example.com/new-qr.png?x=1&y=2')
    const content = panel.emitted('apply')!.at(-1)![0] as EmailBroadcastContent
    const html = new DOMParser().parseFromString(content.message_html, 'text/html')
    const image = html.querySelector('img[data-wechat-qr]')!
    expect(image.getAttribute('src')).toBe('https://example.com/new-qr.png?x=1&y=2')
    expect(image.closest('a')?.getAttribute('href')).toBe(image.getAttribute('src'))
    await panel.setProps({ content })
    await panel.get('#broadcast-template-name').setValue('最新群二维码')
    api.saveEmailBroadcastTemplate.mockResolvedValue({ ...content, id: 'custom', name: '最新群二维码' })
    await panel.findAll('button')[2]!.trigger('click')
    await flushPromises()
    expect(api.saveEmailBroadcastTemplate.mock.calls[0]![0].message_html).toBe(content.message_html)
    expect(panel.get('#broadcast-wechat-qr').element.value).toBe(image.getAttribute('src'))
    panel.unmount()
  })
  it('rejects unsafe QR URLs without changing the current body', async () => {
    const panel = mountPanel()
    await panel.setProps({ content: { ...panel.props('content'), message_html: rechargeBonusTemplate.message_html } })
    await panel.get('#broadcast-wechat-qr').setValue('javascript:alert(1)')
    expect(panel.emitted('apply')).toBeUndefined()
    expect(panel.get('#broadcast-wechat-qr').attributes('aria-invalid')).toBe('true')
    expect(panel.findAll('button')[6]!.attributes('disabled')).toBeDefined()
    panel.unmount()
  })
  it('uses the selected template QR instead of the current message QR', async () => {
    const saved = { ...rechargeBonusTemplate, id: 'saved', message_html: replaceWechatQrUrl(rechargeBonusTemplate.message_html, 'https://example.com/b.png') }
    api.listEmailBroadcastTemplates.mockResolvedValue([saved])
    const panel = mountPanel()
    await panel.setProps({ content: { ...panel.props('content'), message_html: replaceWechatQrUrl(rechargeBonusTemplate.message_html, 'https://example.com/a.png') } })
    await flushPromises()
    await panel.get('select').setValue('saved')
    expect(panel.get('#broadcast-wechat-qr').element.value).toBe('https://example.com/b.png')
    await panel.findAll('button')[0]!.trigger('click')
    const content = panel.emitted('apply')!.at(-1)![0] as EmailBroadcastContent
    expect(getWechatQrUrl(content.message_html)).toBe('https://example.com/b.png')
    panel.unmount()
  })
  it('reports invalid QR edits and clears the old preview until the URL is corrected', async () => {
    const panel = mountPanel()
    await panel.setProps({ content: { ...panel.props('content'), message_html: rechargeBonusTemplate.message_html } })
    await panel.findAll('button')[6]!.trigger('click')
    await flushPromises()
    expect(panel.find('iframe').exists()).toBe(true)
    await panel.get('#broadcast-wechat-qr').setValue('')
    expect(panel.emitted('validity')!.at(-1)).toEqual([false])
    expect(panel.find('iframe').exists()).toBe(false)
    await panel.get('#broadcast-wechat-qr').setValue('https://example.com/new.png')
    expect(panel.emitted('validity')!.at(-1)).toEqual([true])
    panel.unmount()
  })
  it('isolates preview and invalidates it after editing', async () => {
    const panel = mountPanel()
    await flushPromises()
    await panel.findAll('button')[6]!.trigger('click')
    await flushPromises()
    expect(panel.get('iframe').attributes('sandbox')).toBe('')
    expect(panel.get('iframe').attributes('srcdoc')).toContain('Content-Security-Policy')
    await panel.setProps({ content: { ...panel.props('content'), message_html: '修改内容' } })
    expect(panel.find('iframe').exists()).toBe(false)
    panel.unmount()
  })
  it('discards a preview response for content that has changed', async () => {
    let resolve!: (value: { subject: string; html: string }) => void
    api.previewEmailBroadcastContent.mockReturnValue(new Promise(r => { resolve = r }))
    const panel = mountPanel()
    await flushPromises()
    await panel.findAll('button')[6]!.trigger('click')
    await panel.setProps({ content: { ...panel.props('content'), message_html: '新内容' } })
    resolve({ subject: '过期', html: '<p>旧内容</p>' })
    await flushPromises()
    expect(panel.find('iframe').exists()).toBe(false)
    panel.unmount()
  })
  it('discards an in-flight preview when the QR input becomes invalid without a body change', async () => {
    let resolve!: (value: { subject: string; html: string }) => void
    api.previewEmailBroadcastContent.mockReturnValue(new Promise(r => { resolve = r }))
    const panel = mountPanel()
    await panel.setProps({ content: { ...panel.props('content'), message_html: rechargeBonusTemplate.message_html } })
    await panel.findAll('button')[6]!.trigger('click')
    await panel.get('#broadcast-wechat-qr').setValue('invalid')
    resolve({ subject: '过期', html: '<p>旧二维码</p>' })
    await flushPromises()
    expect(panel.find('iframe').exists()).toBe(false)
    panel.unmount()
  })
  it('removes scripts, handlers and active elements while preserving email styling', () => {
    const document = createEmailPreviewDocument('<html><head><style>p{color:red}</style></head><body><script>alert(1)</script><img onerror="alert(1)" src="https://example.com/a.png"><iframe src="https://example.com"></iframe><a href="javascript:alert(1)">链接</a></body></html>')
    expect(document).not.toContain('<script')
    expect(document).not.toContain('onerror')
    expect(document).not.toContain('<iframe')
    expect(document).not.toContain('javascript:')
    expect(document).toContain('p{color:red}')
  })
})
