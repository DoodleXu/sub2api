import { describe, expect, it } from 'vitest'
import {
  buildGhcrImageName,
  buildScriptRollbackCommand,
  normalizeUpdateRepository
} from '@/utils/versionRollback'

describe('version rollback command helpers', () => {
  it('uses the configured fork repository for script rollback commands', () => {
    expect(buildScriptRollbackCommand('DoodleXu/sub2api', '0.1.207')).toBe(
      'curl -sSL https://raw.githubusercontent.com/DoodleXu/sub2api/v0.1.207/deploy/install.sh | sudo bash -s -- rollback v0.1.207'
    )
  })

  it('uses the configured fork repository for GHCR image names', () => {
    expect(buildGhcrImageName('DoodleXu/sub2api')).toBe('ghcr.io/doodlexu/sub2api')
  })

  it('normalizes GitHub URLs and falls back to the fork repository for invalid values', () => {
    expect(normalizeUpdateRepository('https://github.com/DoodleXu/sub2api.git')).toBe(
      'DoodleXu/sub2api'
    )
    expect(normalizeUpdateRepository('sub2api')).toBe('DoodleXu/sub2api')
  })
})
