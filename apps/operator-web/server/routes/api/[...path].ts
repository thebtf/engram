import { createError, getRequestURL, proxyRequest } from 'h3'

export default defineEventHandler(async (event) => {
  const config = useRuntimeConfig(event)
  const targetBase = config.engramApiTarget
  const url = getRequestURL(event)
  let ingress = new URL(targetBase)
  if (url.pathname !== '/api/ready') {
    const publicOrigin = String(config.engramPublicOrigin || '').trim()
    try {
      ingress = new URL(publicOrigin)
    } catch {
      throw createError({ statusCode: 500, statusMessage: 'Invalid NUXT_ENGRAM_PUBLIC_ORIGIN' })
    }
    if (!['http:', 'https:'].includes(ingress.protocol) || ingress.origin !== publicOrigin) {
      throw createError({ statusCode: 500, statusMessage: 'Invalid NUXT_ENGRAM_PUBLIC_ORIGIN' })
    }
    if (event.node.req.headers.host?.toLowerCase() !== ingress.host.toLowerCase()) {
      throw createError({ statusCode: 403, statusMessage: 'Unrecognized operator web origin' })
    }
  }

  const pathWithQuery = `${url.pathname}${url.search}`
  const target = new URL(pathWithQuery, targetBase)

  for (const name of Object.keys(event.node.req.headers)) {
    if (name.toLowerCase().startsWith('x-authentik-')) delete event.node.req.headers[name]
  }

  const publicHost = ingress.host
  const publicProto = ingress.protocol.slice(0, -1)
  const peerIP = event.node.req.socket.remoteAddress || ''
  return proxyRequest(event, target.toString(), {
    headers: {
      'x-forwarded-host': publicHost,
      'x-forwarded-proto': publicProto,
      'x-forwarded-for': peerIP,
      'x-real-ip': peerIP,
      'true-client-ip': peerIP,
    },
  })
})
