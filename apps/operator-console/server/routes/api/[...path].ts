import { getRouterParam, proxyRequest, createError, getRequestURL } from 'h3'

export default defineEventHandler((event) => {
  const config = useRuntimeConfig()
  const upstream = String(config.operatorApiTarget || '').trim()

  if (!/^https?:\/\//i.test(upstream)) {
    throw createError({
      statusCode: 500,
      statusMessage: 'NUXT_OPERATOR_API_TARGET is not configured as an absolute HTTP(S) URL',
    })
  }

  const path = getRouterParam(event, 'path') || ''
  const target = new URL(upstream)
  const requestUrl = getRequestURL(event)
  const publicOrigin = String(config.operatorPublicOrigin || '').trim()
  let ingress = target
  const cleanBase = target.pathname.replace(/\/+$/, '')
  const cleanPath = String(path).replace(/^\/+/, '')
  if (publicOrigin && cleanPath !== 'ready') {
    try {
      ingress = new URL(publicOrigin)
    } catch {
      throw createError({ statusCode: 500, statusMessage: 'Invalid NUXT_OPERATOR_PUBLIC_ORIGIN' })
    }
    if (!['http:', 'https:'].includes(ingress.protocol) || ingress.origin !== publicOrigin) {
      throw createError({ statusCode: 500, statusMessage: 'Invalid NUXT_OPERATOR_PUBLIC_ORIGIN' })
    }
    if (requestUrl.host.toLowerCase() !== ingress.host.toLowerCase()) {
      throw createError({ statusCode: 403, statusMessage: 'Unrecognized operator console origin' })
    }
  }
  const apiBase = /\/api$/i.test(cleanBase) ? cleanBase : `${cleanBase}/api`

  target.pathname = cleanPath ? `${apiBase}/${cleanPath}` : apiBase || '/api'
  target.search = requestUrl.search

  // h3 merges proxyRequest's headers with incoming headers; undefined does not remove an incoming value.
  for (const name of Object.keys(event.node.req.headers)) {
    if (name.toLowerCase().startsWith('x-authentik-')) delete event.node.req.headers[name]
  }

  return proxyRequest(event, target.toString(), {
    headers: {
      'x-forwarded-host': ingress.host,
      'x-forwarded-proto': ingress.protocol.slice(0, -1),
      'x-forwarded-for': event.node.req.socket.remoteAddress || '',
      'x-real-ip': event.node.req.socket.remoteAddress || '',
      'true-client-ip': event.node.req.socket.remoteAddress || '',
    },
  })
})
