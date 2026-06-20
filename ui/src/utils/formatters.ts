/**
 * Converts a Unix-epoch number or ISO date string to a human-readable
 * relative label such as "5m ago" or "2h ago". Values older than 7 days
 * fall back to the locale date string.
 */
export function formatRelativeTime(dateOrEpoch: string | number, locale = 'en'): string {
  const timestamp = typeof dateOrEpoch === 'number' ? dateOrEpoch : new Date(dateOrEpoch).getTime()
  const now = Date.now()
  const diff = now - timestamp
  const isRu = locale.toLowerCase().startsWith('ru')

  const seconds = Math.floor(diff / 1000)
  const minutes = Math.floor(seconds / 60)
  const hours = Math.floor(minutes / 60)
  const days = Math.floor(hours / 24)

  if (seconds < 60) return isRu ? 'только что' : 'just now'
  if (minutes < 60) return isRu ? `${minutes}м назад` : `${minutes}m ago`
  if (hours < 24) return isRu ? `${hours}ч назад` : `${hours}h ago`
  if (days < 7) return isRu ? `${days}д назад` : `${days}d ago`

  return new Date(timestamp).toLocaleDateString(isRu ? 'ru-RU' : locale)
}

/**
 * Converts a Go duration string (e.g. "1h30m45.123456789s") into a compact
 * display form suitable for dashboard uptime widgets.
 */
export function formatUptime(uptimeStr: string): string {
  // Parse Go duration string (e.g., "1h30m45.123456789s")
  const match = uptimeStr.match(/(?:(\d+)h)?(?:(\d+)m)?(?:(\d+(?:\.\d+)?)s)?/)
  if (!match) return uptimeStr

  const hours = parseInt(match[1] || '0', 10)
  const minutes = parseInt(match[2] || '0', 10)
  const seconds = Math.floor(parseFloat(match[3] || '0'))

  if (hours > 0) {
    return `${hours}h ${minutes}m`
  }
  if (minutes > 0) {
    return `${minutes}m ${seconds}s`
  }
  return `${seconds}s`
}

/**
 * Wraps formatRelativeTime with null/undefined/zero-value safety. Returns the
 * em-dash placeholder (—) for any absent or un-parseable value.
 */
export function safeDateFormat(value: string | number | null | undefined, locale = 'en'): string {
  if (value === null || value === undefined || value === '' || value === 0) return '—'
  const date = new Date(typeof value === 'number' ? value : value)
  if (isNaN(date.getTime())) return '—'
  return formatRelativeTime(date.getTime(), locale)
}

/**
 * Like safeDateFormat but returns a full locale date string instead of a
 * relative label. Returns the em-dash placeholder for absent or invalid values.
 */
export function safeAbsoluteDate(value: string | number | null | undefined, locale = 'en-US'): string {
  if (value === null || value === undefined || value === '' || value === 0) return '—'
  const date = new Date(typeof value === 'number' ? value : value)
  if (isNaN(date.getTime())) return '—'
  return date.toLocaleDateString(locale, { year: 'numeric', month: 'short', day: 'numeric' })
}

/**
 * Truncates a string to maxLength, appending an ellipsis when the input
 * exceeds the limit.
 */
export function truncate(text: string, maxLength: number): string {
  if (text.length <= maxLength) return text
  return text.slice(0, maxLength - 3) + '...'
}

/**
 * Replaces HTML special characters with their entity equivalents to prevent
 * injection when rendering user-supplied strings as markup.
 */
export function escapeHtml(text: string): string {
  const map: Record<string, string> = {
    '&': '&amp;',
    '<': '&lt;',
    '>': '&gt;',
    '"': '&quot;',
    "'": '&#039;'
  }
  return text.replace(/[&<>"']/g, m => map[m])
}

/**
 * Parses a JSON string or passes through an already-decoded array. Returns
 * the fallback value when the input is not parseable as JSON.
 */
export function parseJsonSafe<T>(value: unknown, fallback: T): T {
  if (Array.isArray(value)) return value as T
  if (typeof value === 'string') {
    try {
      return JSON.parse(value) as T
    } catch {
      return fallback
    }
  }
  return fallback
}

/**
 * Coerces an unknown value to string, returning an empty string for null or
 * undefined inputs.
 */
export function getString(value: unknown): string {
  if (typeof value === 'string') return value
  if (value === null || value === undefined) return ''
  return String(value)
}
