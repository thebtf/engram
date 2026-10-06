import { createServer, request as httpRequest } from 'node:http'
import { request as httpsRequest } from 'node:https'
import { createReadStream } from 'node:fs'
import { stat } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { extname, relative, resolve, sep } from 'node:path'

const publicDir = fileURLToPath(new URL('../public/', import.meta.url))
const apiTarget = process.env.ENGRAM_OPERATOR_API_TARGET || ''
const runtimeConfig = JSON.stringify({
  apiBase: process.env.ENGRAM_PUBLIC_API_BASE || '/api',
  apiDisplayHost: process.env.ENGRAM_PUBLIC_API_DISPLAY_HOST || '',
})
const mime = { '.html': 'text/html; charset=utf-8', '.js': 'application/javascript; charset=utf-8', '.css': 'text/css; charset=utf-8', '.json': 'application/json', '.svg': 'image/svg+xml', '.png': 'image/png', '.jpg': 'image/jpeg', '.ico': 'image/x-icon', '.woff': 'font/woff', '.woff2': 'font/woff2' }
const ignoredHeaders = { 'transfer-encoding': true, 'accept-encoding': true, connection: true, 'keep-alive': true, upgrade: true, expect: true, host: true, accept: true }

const server = createServer(async (req, res) => {
  try {
    const url = new URL(req.url, 'http://console.invalid')
    if (url.pathname === '/api' || url.pathname.startsWith('/api/')) {
      if (!/^https?:\/\//i.test(apiTarget)) {
        res.writeHead(503).end('ENGRAM_OPERATOR_API_TARGET must be an absolute HTTP(S) URL')
        return
      }
      const target = new URL(apiTarget)
      const base = target.pathname.replace(/\/+$/, '')
      target.pathname = `${/\/api$/i.test(base) ? base : `${base}/api`}${url.pathname.slice(4)}`
      target.search = url.search
      const headers = Object.fromEntries(Object.entries(req.headers).filter(([key]) => !ignoredHeaders[key]))
      const upstream = (target.protocol === 'https:' ? httpsRequest : httpRequest)(target, { method: req.method, headers }, (response) => {
        res.writeHead(response.statusCode, response.headers)
        response.pipe(res)
        response.on('error', () => res.destroy())
      })
      upstream.on('error', () => {
        if (res.headersSent) res.destroy()
        else res.writeHead(502).end('Backend unavailable')
      })
      req.on('aborted', () => upstream.destroy())
      res.on('close', () => upstream.destroy())
      req.pipe(upstream)
      return
    }
    if (req.method !== 'GET' && req.method !== 'HEAD') {
      res.writeHead(405, { Allow: 'GET, HEAD' }).end()
      return
    }
    res.setHeader('Cache-Control', 'no-cache, no-store, must-revalidate')
    res.setHeader('X-Content-Type-Options', 'nosniff')
    if (url.pathname === '/_nuxt/operator-config.js') {
      res.setHeader('Content-Type', mime['.js'])
      res.end(req.method === 'HEAD' ? undefined : `window.engramConsoleConfig=${runtimeConfig};`)
      return
    }
    const pathname = decodeURIComponent(url.pathname)
    const path = resolve(publicDir, `.${pathname}`)
    const rel = relative(publicDir, path)
    if (rel.startsWith('..') || rel.startsWith(sep) || pathname.split('/').some((part) => part.startsWith('.'))) {
      res.writeHead(404).end()
      return
    }
    const file = extname(pathname) ? path : resolve(publicDir, 'index.html')
    if (!(await stat(file)).isFile()) {
      res.writeHead(404).end()
      return
    }
    res.setHeader('Content-Type', mime[extname(file)] || 'application/octet-stream')
    if (extname(file) === '.html') {
      res.setHeader('Content-Security-Policy', "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; object-src 'none'; base-uri 'self'; connect-src 'self'; img-src 'self' data:; font-src 'self'; frame-ancestors 'none'")
      res.setHeader('X-Frame-Options', 'DENY')
      res.setHeader('Referrer-Policy', 'strict-origin-when-cross-origin')
    }
    if (req.method === 'HEAD') res.end()
    else createReadStream(file).on('error', () => res.destroy()).pipe(res)
  } catch (error) {
    res.writeHead(error?.code === 'ENOENT' ? 404 : 400).end()
  }
})
server.listen(Number(process.env.PORT || 3000), process.env.HOST || '0.0.0.0', () => {
  console.log(`Operator console listening on ${process.env.HOST || '0.0.0.0'}:${process.env.PORT || 3000}`)
})
