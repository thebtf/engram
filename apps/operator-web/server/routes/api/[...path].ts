import { getRequestURL, proxyRequest } from 'h3'

export default defineEventHandler(async (event) => {
  const targetBase = useRuntimeConfig(event).engramApiTarget
  const url = getRequestURL(event)

  const pathWithQuery = `${url.pathname}${url.search}`
  const target = new URL(pathWithQuery, targetBase)

  return proxyRequest(event, target.toString())
})
