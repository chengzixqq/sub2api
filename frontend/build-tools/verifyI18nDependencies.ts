import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { resolve } from 'node:path'

/** Keep raw-message JIT compilation aligned with the compiler used by locale checks. */
export function verifyI18nDependencies(frontendRoot: string): void {
  const projectManifest = resolve(frontendRoot, 'package.json')
  const project = JSON.parse(readFileSync(projectManifest, 'utf8'))
  const expected = project.devDependencies?.['@intlify/message-compiler']
  const repair = 'Run pnpm install --frozen-lockfile in frontend before building.'

  if (typeof expected !== 'string' || !/^\d+\.\d+\.\d+(?:-[\w.-]+)?$/.test(expected)) {
    throw new Error(`[i18n dependencies] @intlify/message-compiler must have an exact version. ${repair}`)
  }

  function check(name: string, importer: string): string {
    let manifestPath: string
    let actual: string
    try {
      manifestPath = createRequire(importer).resolve(`${name}/package.json`)
      actual = JSON.parse(readFileSync(manifestPath, 'utf8')).version
    } catch {
      throw new Error(`[i18n dependencies] Cannot resolve ${name} from ${importer}. ${repair}`)
    }
    if (actual !== expected) {
      throw new Error(`[i18n dependencies] ${name}: expected ${expected}, installed ${actual} (${manifestPath}). ${repair}`)
    }
    return manifestPath
  }

  // Resolve from each actual importer, including pnpm's nested dependency tree.
  // A correct top-level compiler alone cannot validate the runtime's compiler.
  const runtime = check('vue-i18n', projectManifest)
  const core = check('@intlify/core-base', runtime)
  const runtimeCompiler = check('@intlify/message-compiler', core)
  const testCompiler = check('@intlify/message-compiler', projectManifest)
  for (const importer of [runtime, core, runtimeCompiler, testCompiler]) {
    check('@intlify/shared', importer)
  }
}
