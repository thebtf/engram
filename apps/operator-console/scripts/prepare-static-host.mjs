import { copyFileSync, mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { resolveBuildPaths } from './build-paths.mjs'

const { appRoot, serverDir } = resolveBuildPaths(process.env.ENGRAM_OPERATOR_OUTPUT_ROOT)
mkdirSync(serverDir, { recursive: true })
copyFileSync(join(appRoot, 'scripts', 'static-host.mjs'), join(serverDir, 'index.mjs'))
