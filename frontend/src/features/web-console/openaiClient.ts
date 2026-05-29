import type {
  ChatResponseMode,
  WebConsoleImageOptions,
  ImageResponseMode,
  WebConsoleImage,
  WebConsoleImageResult,
  WebConsoleMessage,
  WebConsoleRequestContext,
  WebConsoleTextResult,
} from './types'

class WebConsoleRequestError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly code?: string,
  ) {
    super(message)
    this.name = 'WebConsoleRequestError'
  }
}

function endpointPath(base: string): string {
  const value = base.trim()
  if (!value) return ''
  try {
    return new URL(value, globalThis.location?.origin || 'http://localhost').pathname.replace(/\/+$/, '').toLowerCase()
  } catch {
    return value.replace(/^https?:\/\/[^/]+/i, '').replace(/\/+$/, '').toLowerCase()
  }
}

export function isWebConsoleOpenAICompatibleEndpoint(base: string): boolean {
  const path = endpointPath(base)
  return !(
    path.endsWith('/v1beta') ||
    path.includes('/v1beta/') ||
    path.endsWith('/antigravity/v1') ||
    path.includes('/antigravity/v1/') ||
    path.endsWith('/antigravity/v1beta') ||
    path.includes('/antigravity/v1beta/')
  )
}

function endpointUrl(base: string, path: string): string {
  const normalized = base.trim().replace(/\/+$/, '')
  if (!normalized) return path
  if (!isWebConsoleOpenAICompatibleEndpoint(normalized)) {
    throw new WebConsoleRequestError('网页工作台当前只支持 OpenAI-compatible /v1 端点。请选择主端点或 /v1 兼容端点。', 400)
  }
  if (normalized.endsWith('/v1')) return `${normalized}${path}`
  return `${normalized}/v1${path}`
}

function isFatalError(error: unknown): boolean {
  if (!(error instanceof WebConsoleRequestError)) return false
  if ([400, 401, 402, 403, 404].includes(error.status)) return true
  const text = `${error.code || ''} ${error.message}`.toLowerCase()
  return (
    text.includes('api_key') ||
    text.includes('invalid api key') ||
    text.includes('insufficient') ||
    text.includes('quota') ||
    text.includes('balance') ||
    text.includes('subscription') ||
    text.includes('not supported')
  )
}

async function postJSON<T>(endpoint: string, apiKey: string, path: string, payload: unknown): Promise<T> {
  const response = await fetch(endpointUrl(endpoint, path), {
    method: 'POST',
    headers: {
      Authorization: `Bearer ${apiKey}`,
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(payload),
  })

  const contentType = response.headers.get('content-type') || ''
  const body = contentType.includes('application/json')
    ? await response.json().catch(() => null)
    : await response.text().catch(() => '')

  if (!response.ok) {
    const record = (body && typeof body === 'object') ? body as Record<string, any> : {}
    const errorBody = record.error && typeof record.error === 'object' ? record.error as Record<string, any> : record
    const message = String(errorBody.message || record.message || body || `HTTP ${response.status}`)
    const code = typeof errorBody.code === 'string' ? errorBody.code : undefined
    throw new WebConsoleRequestError(message, response.status, code)
  }

  return body as T
}

function contentText(content: unknown): string {
  if (typeof content === 'string') return content
  if (!Array.isArray(content)) return ''
  return content
    .map((part) => {
      if (typeof part === 'string') return part
      if (!part || typeof part !== 'object') return ''
      const record = part as Record<string, any>
      return String(record.text || record.content || '')
    })
    .filter(Boolean)
    .join('\n')
}

function chatMessages(history: WebConsoleMessage[], prompt: string) {
  return [
    ...history
      .filter((message) => message.content.trim())
      .map((message) => ({ role: message.role, content: message.content })),
    { role: 'user', content: prompt },
  ]
}

function responseInput(history: WebConsoleMessage[], prompt: string) {
  return chatMessages(history, prompt).map((message) => ({
    role: message.role,
    content: [{ type: message.role === 'user' ? 'input_text' : 'output_text', text: message.content }],
  }))
}

function extractResponseText(body: any): string {
  if (typeof body?.output_text === 'string') return body.output_text
  const output = Array.isArray(body?.output) ? body.output : []
  for (const item of output) {
    const text = contentText(item?.content)
    if (text) return text
  }
  return ''
}

async function sendResponsesChat(ctx: WebConsoleRequestContext): Promise<string> {
  const body = await postJSON<any>(ctx.endpoint, ctx.apiKey, '/responses', {
    model: ctx.model,
    input: responseInput(ctx.history, ctx.prompt),
  })
  return extractResponseText(body) || '请求已完成，但没有返回文本内容。'
}

async function sendChatCompletions(ctx: WebConsoleRequestContext): Promise<string> {
  const body = await postJSON<any>(ctx.endpoint, ctx.apiKey, '/chat/completions', {
    model: ctx.model,
    messages: chatMessages(ctx.history, ctx.prompt),
  })
  const text = contentText(body?.choices?.[0]?.message?.content)
  return text || '请求已完成，但没有返回文本内容。'
}

export async function sendWebConsoleChat(
  ctx: WebConsoleRequestContext,
  mode: ChatResponseMode,
): Promise<WebConsoleTextResult> {
  const order: Array<Exclude<ChatResponseMode, 'auto'>> =
    mode === 'chat' ? ['chat'] : mode === 'responses' ? ['responses'] : ['responses', 'chat']
  let lastError: unknown

  for (const current of order) {
    try {
      const text = current === 'responses'
        ? await sendResponsesChat(ctx)
        : await sendChatCompletions(ctx)
      return { text, usedMode: current, fallbackUsed: current !== order[0] }
    } catch (error) {
      lastError = error
      if (mode !== 'auto' || isFatalError(error)) break
    }
  }

  throw lastError
}

function imageFromValue(value: string, alt: string): WebConsoleImage {
  if (value.startsWith('http') || value.startsWith('data:')) return { url: value, alt }
  return { url: `data:image/png;base64,${value}`, alt }
}

function normalizedImageOptions(options?: WebConsoleImageOptions): WebConsoleImageOptions {
  return {
    size: options?.size?.trim() || '',
    quality: options?.quality?.trim() || '',
    background: options?.background?.trim() || '',
    outputFormat: options?.outputFormat?.trim() || 'png',
    count: Math.min(Math.max(Math.trunc(Number(options?.count) || 1), 1), 4),
  }
}

function imageGenerationPayload(ctx: WebConsoleRequestContext): Record<string, unknown> {
  const options = normalizedImageOptions(ctx.imageOptions)
  const payload: Record<string, unknown> = {
    prompt: ctx.prompt,
    n: options.count,
    response_format: 'b64_json',
  }

  const model = ctx.model.trim()
  if (model && model !== 'gpt-image-2') {
    payload.model = model
  }
  if (options.size) payload.size = options.size
  if (options.quality) payload.quality = options.quality
  if (options.background) payload.background = options.background
  if (options.outputFormat && options.outputFormat !== 'png') {
    payload.output_format = options.outputFormat
  }

  return payload
}

function collectImages(value: unknown, alt: string, out: WebConsoleImage[]): void {
  if (!value || typeof value !== 'object') return
  if (Array.isArray(value)) {
    for (const item of value) collectImages(item, alt, out)
    return
  }

  const record = value as Record<string, any>
  for (const key of ['b64_json', 'image_url', 'url', 'result']) {
    if (typeof record[key] === 'string') out.push(imageFromValue(record[key], alt))
  }
  for (const child of Object.values(record)) {
    if (child && typeof child === 'object') collectImages(child, alt, out)
  }
}

async function generateImages(ctx: WebConsoleRequestContext): Promise<WebConsoleImageResult> {
  const body = await postJSON<any>(ctx.endpoint, ctx.apiKey, '/images/generations', imageGenerationPayload(ctx))
  const images: WebConsoleImage[] = []
  collectImages(body?.data || body, ctx.prompt, images)
  if (images.length === 0) throw new WebConsoleRequestError('生图接口未返回图片。', 502)
  return { images, usedMode: 'images', fallbackUsed: false }
}

async function generateImagesWithResponses(ctx: WebConsoleRequestContext): Promise<WebConsoleImageResult> {
  const options = normalizedImageOptions(ctx.imageOptions)
  const requestBody = {
    model: ctx.model,
    input: ctx.prompt,
    tools: [{
      type: 'image_generation',
      ...(options.size ? { size: options.size } : {}),
      ...(options.quality ? { quality: options.quality } : {}),
      ...(options.background ? { background: options.background } : {}),
      ...(options.outputFormat && options.outputFormat !== 'png' ? { output_format: options.outputFormat } : {}),
    }],
  }
  const images: WebConsoleImage[] = []
  const texts: string[] = []

  for (let index = 0; index < options.count; index++) {
    const body = await postJSON<any>(ctx.endpoint, ctx.apiKey, '/responses', requestBody)
    collectImages(body, ctx.prompt, images)
    const text = extractResponseText(body)
    if (text) texts.push(text)
  }

  if (images.length === 0) throw new WebConsoleRequestError('Responses 未返回图片。', 502)
  return {
    images,
    text: texts.join('\n\n'),
    usedMode: 'responses',
    fallbackUsed: false,
  }
}

export async function generateWebConsoleImage(
  ctx: WebConsoleRequestContext,
  mode: ImageResponseMode,
): Promise<WebConsoleImageResult> {
  const order: Array<Exclude<ImageResponseMode, 'auto'>> =
    mode === 'responses' ? ['responses'] : mode === 'images' ? ['images'] : ['images', 'responses']
  let lastError: unknown

  for (const current of order) {
    try {
      const result = current === 'images'
        ? await generateImages(ctx)
        : await generateImagesWithResponses(ctx)
      return { ...result, fallbackUsed: current !== order[0] }
    } catch (error) {
      lastError = error
      if (mode !== 'auto' || isFatalError(error)) break
    }
  }

  throw lastError
}

export function webConsoleErrorMessage(error: unknown): string {
  if (error instanceof Error && error.message) return error.message
  return '请求失败，请稍后重试。'
}
