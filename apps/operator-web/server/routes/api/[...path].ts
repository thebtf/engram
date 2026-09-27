import { getRequestURL, proxyRequest } from 'h3'

export default defineEventHandler(async (event) => {
  const targetBase = useRuntimeConfig(event).engramApiTarget
  const url = getRequestURL(event)

  const pathWithQuery = `${url.pathname}${url.search}`
  const target = new URL(pathWithQuery, targetBase)

  for (const name of Object.keys(event.node.req.headers)) {
    if (name.toLowerCase().startsWith('x-authentik-')) delete event.node.req.headers[name]
  }

  return proxyRequest(event, target.toString())
})
