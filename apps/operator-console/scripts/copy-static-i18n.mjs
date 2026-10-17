#!/usr/bin/env node
import { cpSync, existsSync, mkdirSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import { resolveBuildPaths } from './build-paths.mjs'

const { appRoot, publicDir, outputPublicDir, overridden } = resolveBuildPaths(process.env.ENGRAM_OPERATOR_OUTPUT_ROOT)
const sourceDir = join(appRoot, 'i18n', 'locales')
const publicTargetDir = join(publicDir, 'i18n', 'locales')
const outputTargetDir = join(outputPublicDir, 'i18n', 'locales')

if (!existsSync(sourceDir)) {
  console.error(`copy-static-i18n: source locale dir not found: ${sourceDir}`)
  process.exit(1)
}

if (overridden) {
  rmSync(publicDir, { recursive: true, force: true })
  cpSync(join(appRoot, 'public'), publicDir, { recursive: true, force: true })
}

mkdirSync(publicTargetDir, { recursive: true })
cpSync(sourceDir, publicTargetDir, { recursive: true, force: true })
console.log(`copy-static-i18n: synced locales to ${publicTargetDir}`)

if (!overridden && existsSync(outputPublicDir)) {
  mkdirSync(outputTargetDir, { recursive: true })
  cpSync(sourceDir, outputTargetDir, { recursive: true, force: true })
  console.log(`copy-static-i18n: synced locales to ${outputTargetDir}`)
}
