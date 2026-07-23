export type WebConsoleRole = 'user' | 'assistant'

export type WebConsoleMode = 'chat' | 'image'

export type WebConsoleImageTaskMode = 'generate' | 'edit'

export type WebConsoleResponseMode = 'responses'

export interface WebConsoleImageReference {
  data_url: string
  name?: string
  cacheKey?: string
}

export interface WebConsoleImageOptions {
  size: string
  quality: string
  background: string
  outputFormat: string
  count: number
  ratio?: string
  outputCompression?: number | null
  inputFidelity?: string
}

export interface WebConsoleImage {
  url: string
  alt?: string
  assetId?: string | number
  cacheKey?: string
  sha256?: string
  mimeType?: string
  extension?: string
  unavailable?: boolean
}

export interface WebConsoleMessage {
  id: string
  role: WebConsoleRole
  content: string
  images?: WebConsoleImage[]
  imageRequest?: WebConsoleImageRequest
  imageTaskId?: string | number
  imageTaskApiKeyId?: number
  imageTaskEndpoint?: string
  status?: 'pending' | 'running' | 'processing' | 'completed' | 'failed'
  created_at: string
}

export interface WebConsoleSession {
  id: string
  title: string
  mode: WebConsoleMode
  messages: WebConsoleMessage[]
  created_at: string
  updated_at: string
}

export interface WebConsoleRequestContext {
  endpoint: string
  apiKey: string
  model: string
  prompt: string
  history: WebConsoleMessage[]
  tools?: unknown[]
  toolChoice?: unknown
}

export interface WebConsoleTextResult {
  text: string
  usedMode: WebConsoleResponseMode
  images?: WebConsoleImage[]
}

export interface WebConsoleImageRequest {
  prompt: string
  mode?: WebConsoleImageTaskMode
  model: string
  options: WebConsoleImageOptions
  referenceImages?: WebConsoleImageReference[]
  maskImage?: WebConsoleImageReference | null
}
