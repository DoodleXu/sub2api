import { describe, expect, it } from 'vitest'
import { planValiditySuffix } from '../validity'

const t = (key: string): string =>
  ({
    'payment.day': '天',
    'payment.days': '天',
    'payment.week': '周',
    'payment.weeks': '周',
    'payment.month': '个月',
    'payment.months': '个月',
    'payment.year': '年',
    'payment.years': '年',
  })[key] ?? key

const suffix = (validity_days: number, validity_unit: string) =>
  planValiditySuffix({ validity_days, validity_unit }, t)

describe('planValiditySuffix', () => {
  // #4607：管理端表单保存的是复数 'months'，此前用户侧只匹配单数 'month'，
  // 「1 个月」的套餐被显示成「1天」。
  it('renders admin-form plural months correctly', () => {
    expect(suffix(1, 'months')).toBe('1个月')
    expect(suffix(3, 'months')).toBe('3个月')
  })

  it('renders singular month the same way', () => {
    expect(suffix(1, 'month')).toBe('1个月')
    expect(suffix(6, 'month')).toBe('6个月')
  })

  // 计费侧 weeks 按 ×7 天换算；显示必须是周数而非天数。
  it('renders weeks as weeks instead of mislabeled days', () => {
    expect(suffix(2, 'weeks')).toBe('2周')
    expect(suffix(1, 'week')).toBe('1周')
  })

  it('renders day-based and legacy units as days', () => {
    expect(suffix(30, 'days')).toBe('30天')
    expect(suffix(30, 'day')).toBe('30天') // 数据库默认值
    expect(suffix(30, '')).toBe('30天')
  })

  it('matches fork year billing and falls back unknown units to days', () => {
    expect(suffix(1, 'year')).toBe('1年')
    expect(suffix(365, 'unknown')).toBe('365天')
  })

  it('normalizes casing and whitespace', () => {
    expect(suffix(1, ' Months ')).toBe('1个月')
    expect(suffix(2, 'WEEKS')).toBe('2周')
  })
})
