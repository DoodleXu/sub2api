export type WebConsoleRole = 'user' | 'assistant'

export type WebConsoleMode = 'chat' | 'image'

export type ChatResponseMode = 'auto' | 'responses' | 'chat'

export type ImageResponseMode = 'auto' | 'images' | 'responses'

export interface WebConsoleImageOptions {
  size: string
  quality: string
  background: string
  outputFormat: string
  count: number
}

export interface WebConsoleImage {
  url: string
  alt?: string
}

export interface WebConsoleMessage {
  id: string
  role: WebConsoleRole
  content: string
  images?: WebConsoleImage[]
  imageRequest?: WebConsoleImageRequest
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
  imageOptions?: WebConsoleImageOptions
}

export interface WebConsoleTextResult {
  text: string
  usedMode: Exclude<ChatResponseMode, 'auto'>
  fallbackUsed: boolean
}

export interface WebConsoleImageResult {
  images: WebConsoleImage[]
  text?: string
  usedMode: Exclude<ImageResponseMode, 'auto'>
  fallbackUsed: boolean
}

export interface WebConsoleImageRequest {
  prompt: string
  model: string
  mode: ImageResponseMode
  options: WebConsoleImageOptions
}
