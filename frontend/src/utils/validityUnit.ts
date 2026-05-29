export type NormalizedValidityUnit = 'days' | 'weeks' | 'months' | 'years'

const VALIDITY_UNIT_ALIASES: Record<string, NormalizedValidityUnit> = {
  day: 'days',
  days: 'days',
  week: 'weeks',
  weeks: 'weeks',
  month: 'months',
  months: 'months',
  year: 'years',
  years: 'years',
}

export function normalizeValidityUnit(unit?: string | null): NormalizedValidityUnit {
  return VALIDITY_UNIT_ALIASES[(unit || '').trim().toLowerCase()] || 'days'
}

export function validityUnitLabelKey(unit?: string | null, value = 2): string {
  const normalized = normalizeValidityUnit(unit)
  if (value === 1) {
    return normalized.slice(0, -1)
  }
  return normalized
}

export function formatValidityPeriod(
  value: number | null | undefined,
  unit: string | null | undefined,
  t: (key: string) => string,
  prefix = 'payment',
  separator = '',
): string {
  const amount = value ?? 1
  return `${amount}${separator}${t(`${prefix}.${validityUnitLabelKey(unit, amount)}`)}`
}
