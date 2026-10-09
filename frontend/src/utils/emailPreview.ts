import DOMPurify from 'dompurify'

export function createEmailPreviewDocument(html: string): string {
  const safeHTML = DOMPurify.sanitize(html, { WHOLE_DOCUMENT: true, ADD_TAGS: ['style'], FORBID_TAGS: ['base', 'form', 'iframe', 'object', 'embed'], FORBID_ATTR: ['href', 'action', 'target'] })
  // Keep the real email styles while disallowing executable content and external styles.
  return safeHTML.replace(/<head[^>]*>/i, `<head><meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src https: http: data:; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'">`)
}
