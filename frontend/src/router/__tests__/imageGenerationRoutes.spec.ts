import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

const routerSource = readFileSync(resolve(dirname(fileURLToPath(import.meta.url)), '../index.ts'), 'utf8')
const sidebarSource = readFileSync(resolve(dirname(fileURLToPath(import.meta.url)), '../../components/layout/AppSidebar.vue'), 'utf8')

describe('image generation admin navigation', () => {
  it('keeps the old address and redirects it to results', () => {
    expect(routerSource).toContain("path: '/admin/image-generations',")
    expect(routerSource).toContain("redirect: '/admin/image-generations/results'")
    expect(routerSource).toContain("path: '/admin/image-generations/queue'")
    expect(routerSource).toContain("path: '/admin/image-generations/results'")
  })

  it('exposes queue and results as children of the image generation menu', () => {
    expect(sidebarSource).toContain("path: '/admin/image-generations/queue'")
    expect(sidebarSource).toContain("path: '/admin/image-generations/results'")
    expect(sidebarSource).toContain('expandOnly: true')
  })
})
