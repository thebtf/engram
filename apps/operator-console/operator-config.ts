declare global {
  interface Window {
    engramConsoleConfig?: { apiBase?: string; apiDisplayHost?: string }
  }
}
export const operatorConfig = {
  apiBase: window.engramConsoleConfig?.apiBase || '/api',
  apiDisplayHost: window.engramConsoleConfig?.apiDisplayHost || '',
}
