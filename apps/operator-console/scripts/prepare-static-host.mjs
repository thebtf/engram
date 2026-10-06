import { copyFileSync, mkdirSync } from 'node:fs'

mkdirSync('.output/server', { recursive: true })
copyFileSync('scripts/static-host.mjs', '.output/server/index.mjs')
