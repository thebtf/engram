#!/usr/bin/env node
import { existsSync, readdirSync, readFileSync } from 'node:fs'
import { basename, dirname, join, normalize, relative, resolve, sep } from 'node:path'

const publicDir = resolve('.output/public')
const nuxtDir = join(publicDir, '_nuxt')
const assetExtensions = [
  'js',
  'css',
  'json',
  'woff',
  'woff2',
  'svg',
  'png',
  'jpg',
  'jpeg',
  'webp',
  'ico',
]
const assetPattern = assetExtensions.join('|')

if (!existsSync(nuxtDir)) {
  console.error('[check-static-assets] missing .output/public/_nuxt')
  process.exit(1)
}

function walk(dir) {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const fullPath = join(dir, entry.name)
    return entry.isDirectory() ? walk(fullPath) : [fullPath]
  })
}

function isInside(parent, child) {
  const rel = relative(parent, child)
  return rel === '' || (!rel.startsWith('..') && !rel.startsWith(sep))
}

function recordMissing(missing, source, target) {
  missing.push(`${relative(publicDir, source)} -> ${relative(publicDir, target)}`)
}

const files = walk(publicDir)
const missing = []
const relativeAsset = new RegExp(`["']\\.\\/([^"']+\\.(${assetPattern})(?:\\?[^"']*)?)["']`, 'gi')
const absoluteAsset = new RegExp(`["']\\/_nuxt\\/([^"']+\\.(${assetPattern})(?:\\?[^"']*)?)["']`, 'gi')

for (const file of files) {
  if (!/\.(html|js|css|json)$/i.test(file)) {
    continue
  }

  const text = readFileSync(file, 'utf8')
  for (const match of text.matchAll(relativeAsset)) {
    const cleanRef = match[1].split('?')[0]
    const target = normalize(join(dirname(file), cleanRef))
    if (!isInside(publicDir, target) || !existsSync(target)) {
      recordMissing(missing, file, target)
    }
  }
  for (const match of text.matchAll(absoluteAsset)) {
    const cleanRef = match[1].split('?')[0]
    const target = normalize(join(nuxtDir, cleanRef))
    if (!existsSync(target)) {
      recordMissing(missing, file, target)
    }
  }
}

if (missing.length > 0) {
  console.error('[check-static-assets] generated output has missing asset references:')
  for (const item of missing) {
    console.error(`- ${item}`)
  }
  process.exit(1)
}

console.log(`[check-static-assets] OK: ${files.length} generated files checked in ${basename(publicDir)}`)
