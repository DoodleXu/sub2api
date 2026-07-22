import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

const componentPath = resolve(dirname(fileURLToPath(import.meta.url)), '../OpsDashboardHeader.vue')
const componentSource = readFileSync(componentPath, 'utf8')

describe('OpsDashboardHeader image generation average', () => {
  it('always renders the image generation average in green', () => {
    expect(componentSource).toContain(
      '<span class="font-bold text-green-600 dark:text-green-400">{{ imageGenerationTTFTAvgMs ?? \'-\' }}</span>'
    )
    expect(componentSource).not.toContain('getTTFTThresholdLevel(imageGenerationTTFTAvgMs)')
  })
})
