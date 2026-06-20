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
  const cleanBase = target.pathname.replace(/\/+$/, '')
  const cleanPath = String(path).replace(/^\/+/, '')
  const apiBase = /\/api$/i.test(cleanBase) ? cleanBase : `${cleanBase}/api`

  target.pathname = cleanPath ? `${apiBase}/${cleanPath}` : apiBase || '/api'
  target.search = requestUrl.search

  return proxyRequest(event, target.toString())
})
