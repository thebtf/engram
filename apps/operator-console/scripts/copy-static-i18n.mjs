#!/usr/bin/env node
import { cpSync, existsSync, mkdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const scriptDir = dirname(fileURLToPath(import.meta.url))
const appRoot = join(scriptDir, '..')
const sourceDir = join(appRoot, 'i18n', 'locales')
const publicTargetDir = join(appRoot, 'public', 'i18n', 'locales')
const outputTargetDir = join(appRoot, '.output', 'public', 'i18n', 'locales')

if (!existsSync(sourceDir)) {
  console.error(`copy-static-i18n: source locale dir not found: ${sourceDir}`)
  process.exit(1)
}

mkdirSync(publicTargetDir, { recursive: true })
cpSync(sourceDir, publicTargetDir, { recursive: true, force: true })
console.log(`copy-static-i18n: synced locales to ${publicTargetDir}`)

if (existsSync(join(appRoot, '.output', 'public'))) {
  mkdirSync(outputTargetDir, { recursive: true })
  cpSync(sourceDir, outputTargetDir, { recursive: true, force: true })
  console.log(`copy-static-i18n: synced locales to ${outputTargetDir}`)
}
