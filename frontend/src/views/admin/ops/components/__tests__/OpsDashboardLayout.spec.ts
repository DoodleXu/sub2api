import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

const dashboardPath = resolve(dirname(fileURLToPath(import.meta.url)), '../../OpsDashboard.vue')
const dashboardSource = readFileSync(dashboardPath, 'utf8')

describe('OpsDashboard primary chart row layout', () => {
  it('lets both trend panels stretch to the concurrency panel height', () => {
    const rowMatch = dashboardSource.match(
      /data-testid="ops-primary-charts-row"[\s\S]*?<!-- Row: Visual Analysis/
    )

    expect(rowMatch).not.toBeNull()

    const rowSource = rowMatch?.[0] ?? ''
    expect(rowSource).toContain('items-stretch')
    expect(rowSource.match(/min-h-\[360px\]/g)).toHaveLength(3)
    expect(rowSource).not.toMatch(/(?:^|\s)h-\[360px\](?:\s|\")/)
  })
})
