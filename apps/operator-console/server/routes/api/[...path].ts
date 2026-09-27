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
  const ingress = new URL(publicOrigin || upstream)
  const cleanBase = target.pathname.replace(/\/+$/, '')
  const cleanPath = String(path).replace(/^\/+/, '')
  if (publicOrigin && cleanPath !== 'ready' && (ingress.origin !== publicOrigin || requestUrl.host.toLowerCase() !== ingress.host.toLowerCase())) {
    throw createError({ statusCode: 403, statusMessage: 'Unrecognized operator console origin' })
  }
  const apiBase = /\/api$/i.test(cleanBase) ? cleanBase : `${cleanBase}/api`

  target.pathname = cleanPath ? `${apiBase}/${cleanPath}` : apiBase || '/api'
  target.search = requestUrl.search

  return proxyRequest(event, target.toString(), {
    headers: {
      'x-forwarded-host': ingress.host,
      'x-forwarded-proto': ingress.protocol.slice(0, -1),
    },
  })
})
