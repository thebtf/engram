#!/usr/bin/env node
import { cpSync, existsSync, mkdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const scriptDir = dirname(fileURLToPath(import.meta.url))
const appRoot = join(scriptDir, '..')
const sourceDir = join(appRoot, 'i18n', 'locales')
const targetDir = join(appRoot, '.output', 'public', 'i18n', 'locales')

if (!existsSync(sourceDir)) {
  console.error(`copy-static-i18n: source locale dir not found: ${sourceDir}`)
  process.exit(1)
}

mkdirSync(targetDir, { recursive: true })
cpSync(sourceDir, targetDir, { recursive: true, force: true })
console.log(`copy-static-i18n: copied locales to ${targetDir}`)
