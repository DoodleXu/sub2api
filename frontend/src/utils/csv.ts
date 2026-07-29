/**
 * Encode one CSV cell and neutralize spreadsheet formula prefixes in text.
 * Numeric values remain numeric; user-controlled strings are always quoted.
 */
export function escapeSpreadsheetCSVValue(value: unknown): string {
  if (value === null || value === undefined) return ''
  if (typeof value === 'number') return String(value)

  let text = String(value)
  if (/^[\s]*[=+\-@]/.test(text)) text = `'${text}`
  return `"${text.replace(/"/g, '""')}"`
}
