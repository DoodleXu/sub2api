import type { WebConsoleImageOptions, WebConsoleImageReference, WebConsoleImageTaskMode } from '@/features/web-console/types'

export interface CreateAsyncImageTaskRequest {
  endpoint: string
  api_key: string
  mode?: WebConsoleImageTaskMode
  model: string
  prompt: string
  options: WebConsoleImageOptions
  reference_images?: WebConsoleImageReference[]
  mask_image?: WebConsoleImageReference | null
}

export interface AsyncImageTaskAsset {
  id: string
  asset_index: number
  mime_type: string
  extension: string
  bytes: number
  sha256: string
  url: string
}

export interface AsyncImageTask {
  id: string
  task_id: string
  status: 'processing' | 'completed' | 'failed'
  error?: { message?: string } | null
  result?: { data?: Array<{ url?: string; revised_prompt?: string }> } | null
}

function endpointURL(base: string, path: string): string {
  const normalized = base.trim().replace(/\/+$/, '')
  if (normalized.endsWith('/v1')) return `${normalized}${path}`
  return `${normalized}/v1${path}`
}

async function decodeResponse(response: Response): Promise<any> {
  const body = await response.json().catch(() => null)
  if (response.ok) return body
  const message = body?.error?.message || body?.message || `HTTP ${response.status}`
  throw new Error(String(message))
}

function dataURLFile(reference: WebConsoleImageReference, fallbackName: string): File {
  const match = reference.data_url.match(/^data:([^;,]+);base64,(.*)$/)
  if (!match) throw new Error('参考图数据无效，请重新添加。')
  const binary = atob(match[2])
  const bytes = new Uint8Array(binary.length)
  for (let index = 0; index < binary.length; index++) bytes[index] = binary.charCodeAt(index)
  return new File([bytes], reference.name || fallbackName, { type: match[1] })
}

function resolvedSize(options: WebConsoleImageOptions): string {
  if (options.size) return options.size
  if (options.ratio === '1:1') return '1024x1024'
  if (['5:4', '4:3', '3:2', '16:9', '21:9'].includes(options.ratio || '')) return '1536x1024'
  if (['4:5', '3:4', '2:3', '9:16', '9:21'].includes(options.ratio || '')) return '1024x1536'
  return ''
}

function appendOptions(form: FormData, options: WebConsoleImageOptions): void {
  const size = resolvedSize(options)
  if (size) form.append('size', size)
  if (options.quality) form.append('quality', options.quality)
  if (options.background) form.append('background', options.background)
  if (options.outputFormat) form.append('output_format', options.outputFormat)
  if (options.outputCompression != null && options.outputFormat !== 'png') {
    form.append('output_compression', String(options.outputCompression))
  }
  if (options.inputFidelity) form.append('input_fidelity', options.inputFidelity)
  form.append('n', String(Math.min(Math.max(Math.trunc(options.count || 1), 1), 4)))
}

export async function create(request: CreateAsyncImageTaskRequest): Promise<{ task: AsyncImageTask }> {
  const headers = { Authorization: `Bearer ${request.api_key}` }
  let response: Response
  if ((request.mode || 'generate') === 'edit') {
    const references = request.reference_images || []
    if (references.length === 0) throw new Error('编辑模式需要至少一张参考图。')
    const form = new FormData()
    form.append('model', request.model)
    form.append('prompt', request.prompt)
    appendOptions(form, request.options)
    references.forEach((reference, index) => form.append('image', dataURLFile(reference, `reference-${index}.png`)))
    if (request.mask_image) form.append('mask', dataURLFile(request.mask_image, 'mask.png'))
    response = await fetch(endpointURL(request.endpoint, '/images/edits/async'), { method: 'POST', headers, body: form })
  } else {
    const options = request.options
    const payload: Record<string, unknown> = {
      model: request.model,
      prompt: request.prompt,
      n: Math.min(Math.max(Math.trunc(options.count || 1), 1), 4),
    }
    const size = resolvedSize(options)
    if (size) payload.size = size
    if (options.quality) payload.quality = options.quality
    if (options.background) payload.background = options.background
    if (options.outputFormat) payload.output_format = options.outputFormat
    if (options.outputCompression != null && options.outputFormat !== 'png') payload.output_compression = options.outputCompression
    response = await fetch(endpointURL(request.endpoint, '/images/generations/async'), {
      method: 'POST',
      headers: { ...headers, 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    })
  }
  const task = await decodeResponse(response) as AsyncImageTask
  return { task }
}

export async function get(endpoint: string, apiKey: string, id: string): Promise<AsyncImageTask> {
  const response = await fetch(endpointURL(endpoint, `/images/tasks/${encodeURIComponent(id)}`), {
    headers: { Authorization: `Bearer ${apiKey}` },
    cache: 'no-store',
  })
  return decodeResponse(response) as Promise<AsyncImageTask>
}

export function taskAssets(task: AsyncImageTask): AsyncImageTaskAsset[] {
  return (task.result?.data || []).flatMap((item, index) => item.url ? [{
    id: `${task.task_id}-${index}`,
    asset_index: index,
    mime_type: '',
    extension: '',
    bytes: 0,
    sha256: '',
    url: item.url,
  }] : [])
}

export const asyncImageTasksAPI = { create, get, taskAssets }

export default asyncImageTasksAPI
