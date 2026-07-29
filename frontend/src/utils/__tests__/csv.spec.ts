import { describe, expect, it } from 'vitest'

import { escapeSpreadsheetCSVValue } from '../csv'

describe('escapeSpreadsheetCSVValue', () => {
  it('quotes text and doubles embedded quotes', () => {
    expect(escapeSpreadsheetCSVValue('a,"b"')).toBe('"a,""b"""')
  })

  it('neutralizes spreadsheet formula prefixes including leading whitespace', () => {
    expect(escapeSpreadsheetCSVValue('=1+1')).toBe('"\'=1+1"')
    expect(escapeSpreadsheetCSVValue('  @SUM(A1:A2)')).toBe('"\'  @SUM(A1:A2)"')
  })

  it('keeps real numeric values numeric and handles empty cells', () => {
    expect(escapeSpreadsheetCSVValue(-12.5)).toBe('-12.5')
    expect(escapeSpreadsheetCSVValue(null)).toBe('')
  })
})
