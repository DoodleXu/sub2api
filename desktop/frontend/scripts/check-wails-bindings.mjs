import { readdir, readFile } from 'node:fs/promises'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const frontendDir = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const desktopDir = resolve(frontendDir, '..')
const bindingDir = resolve(frontendDir, 'wailsjs/go/main')

const goFiles = (await readdir(desktopDir))
  .filter((name) => name.endsWith('.go') && !name.endsWith('_test.go'))
  .map((name) => resolve(desktopDir, name))

const goSource = (await Promise.all(goFiles.map((file) => readFile(file, 'utf8')))).join('\n')
const goMethods = new Set([...goSource.matchAll(/func\s+\(a\s+\*App\)\s+([A-Z][A-Za-z0-9_]*)\s*\(/g)].map((match) => match[1]))

const declaration = await readFile(resolve(bindingDir, 'App.d.ts'), 'utf8')
const implementation = await readFile(resolve(bindingDir, 'App.js'), 'utf8')
const declared = new Set([...declaration.matchAll(/export function\s+([A-Z][A-Za-z0-9_]*)\s*\(/g)].map((match) => match[1]))
const implemented = new Set([...implementation.matchAll(/export function\s+([A-Z][A-Za-z0-9_]*)\s*\(/g)].map((match) => match[1]))

const missingDeclaration = [...goMethods].filter((name) => !declared.has(name)).sort()
const missingImplementation = [...goMethods].filter((name) => !implemented.has(name)).sort()
const staleDeclaration = [...declared].filter((name) => !goMethods.has(name)).sort()
const staleImplementation = [...implemented].filter((name) => !goMethods.has(name)).sort()

if (missingDeclaration.length || missingImplementation.length || staleDeclaration.length || staleImplementation.length || declared.size !== implemented.size) {
  const report = {
    missingDeclaration,
    missingImplementation,
    staleDeclaration,
    staleImplementation,
    declarationOnly: [...declared].filter((name) => !implemented.has(name)).sort(),
    implementationOnly: [...implemented].filter((name) => !declared.has(name)).sort(),
  }
  console.error(`Wails binding drift detected: ${JSON.stringify(report, null, 2)}`)
  process.exit(1)
}

console.log(`Wails bindings are synchronized (${goMethods.size} App methods).`)
