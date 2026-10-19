import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

export function resolveBuildPaths(outputOverride = '') {
  const appRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..')
  const outputRoot = resolve(appRoot, outputOverride || '.output')
  return {
    appRoot,
    outputRoot,
    overridden: Boolean(outputOverride),
    outputPublicDir: join(outputRoot, 'public'),
    publicDir: outputOverride ? join(outputRoot, 'static-public') : join(appRoot, 'public'),
    cacheDir: join(outputRoot, 'cache'),
    serverDir: join(outputRoot, 'server'),
  }
}
