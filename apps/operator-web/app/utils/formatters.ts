export function formatRelativeTime(dateString: string): string {
  const date = new Date(dateString)
  const diffMs = date.getTime() - Date.now()
  const absMs = Math.abs(diffMs)

  const minute = 60_000
  const hour = 60 * minute
  const day = 24 * hour

  const rtf = new Intl.RelativeTimeFormat('ru', { numeric: 'auto' })

  if (absMs < minute) {
    return 'только что'
  }

  if (absMs < hour) {
    return rtf.format(Math.round(diffMs / minute), 'minute')
  }

  if (absMs < day) {
    return rtf.format(Math.round(diffMs / hour), 'hour')
  }

  return rtf.format(Math.round(diffMs / day), 'day')
}

export function formatAbsoluteDate(dateString: string): string {
  return new Intl.DateTimeFormat('ru-RU', {
    dateStyle: 'medium',
    timeStyle: 'short',
  }).format(new Date(dateString))
}

