// @vitest-environment node
import { afterEach, describe, expect, it } from 'vitest'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { verifyI18nDependencies } from '../../../build-tools/verifyI18nDependencies'

const version = '11.4.16'
const fixtures: string[] = []

function writeManifest(path: string, contents: object): void {
  mkdirSync(dirname(path), { recursive: true })
  writeFileSync(path, JSON.stringify(contents))
}

function fixture(): string {
  const root = mkdtempSync(join(tmpdir(), 'sub2-i18n-dependencies-'))
  fixtures.push(root)
  writeManifest(join(root, 'package.json'), {
    devDependencies: { '@intlify/message-compiler': version }
  })
  for (const name of ['vue-i18n', '@intlify/core-base', '@intlify/message-compiler', '@intlify/shared']) {
    writeManifest(join(root, 'node_modules', name, 'package.json'), { name, version })
  }
  return root
}

afterEach(() => {
  for (const root of fixtures.splice(0)) rmSync(root, { recursive: true, force: true })
})

describe('i18n build dependency guard', () => {
  it('accepts the frozen project installation', () => {
    expect(() => verifyI18nDependencies(fileURLToPath(new URL('../../../', import.meta.url)))).not.toThrow()
  })

  it('accepts matching runtime and compiler dependencies', () => {
    expect(() => verifyI18nDependencies(fixture())).not.toThrow()
  })

  it('rejects stale runtime dependencies before bundling raw messages', () => {
    const root = fixture()
    writeManifest(join(root, 'node_modules/vue-i18n/package.json'), { name: 'vue-i18n', version: '9.14.5' })
    expect(() => verifyI18nDependencies(root)).toThrow(/vue-i18n: expected 11\.4\.16, installed 9\.14\.5.*pnpm install --frozen-lockfile/)
  })

  it('checks the compiler resolved by the runtime instead of only the top-level compiler', () => {
    const root = fixture()
    writeManifest(join(root, 'node_modules/@intlify/core-base/node_modules/@intlify/message-compiler/package.json'), {
      name: '@intlify/message-compiler', version: '9.14.5'
    })
    expect(() => verifyI18nDependencies(root)).toThrow(/@intlify\/message-compiler: expected 11\.4\.16, installed 9\.14\.5/)
  })

  it('rejects a mismatched shared dependency nested under the runtime compiler', () => {
    const root = fixture()
    writeManifest(join(root, 'node_modules/@intlify/message-compiler/node_modules/@intlify/shared/package.json'), {
      name: '@intlify/shared', version: '9.14.5'
    })
    expect(() => verifyI18nDependencies(root)).toThrow(/@intlify\/shared: expected 11\.4\.16, installed 9\.14\.5/)
  })

  it('gives an installation instruction when a required dependency manifest is missing', () => {
    const root = fixture()
    // pnpm's CLI shim adds its shared store to NODE_PATH. Keep the local package
    // present but its exported manifest missing so resolution cannot fall back
    // to an unrelated installation when this test runs through pnpm.
    writeManifest(join(root, 'node_modules/@intlify/core-base/package.json'), {
      name: '@intlify/core-base', version,
      exports: { './package.json': './missing-manifest.json' }
    })
    expect(() => verifyI18nDependencies(root)).toThrow(/Cannot resolve @intlify\/core-base.*pnpm install --frozen-lockfile/)
  })
})
