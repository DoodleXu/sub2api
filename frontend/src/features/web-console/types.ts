export type WebConsoleRole = 'user' | 'assistant'

export type WebConsoleImageTaskMode = 'generate' | 'edit'

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
  mode: 'image'
  messages: WebConsoleMessage[]
  created_at: string
  updated_at: string
}

export interface WebConsoleImageRequest {
  prompt: string
  mode?: WebConsoleImageTaskMode
  model: string
  options: WebConsoleImageOptions
  referenceImages?: WebConsoleImageReference[]
  maskImage?: WebConsoleImageReference | null
}
