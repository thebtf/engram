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

export function shortProjectName(project: string, projectNames: Record<string, string> = {}): string {
  if (!project) return '?'
  if (projectNames[project]) return projectNames[project]
  if (/^[A-Z]:\\/.test(project)) {
    const parts = project.split(/[/\\]/)
    return parts[parts.length - 1] || project
  }
  if (/^[0-9a-f]{6,}$/i.test(project)) {
    return project.slice(0, 8)
  }
  if (/^(.+)_[0-9a-f]{6,}$/i.test(project)) {
    return project.replace(/_[0-9a-f]{6,}$/i, '')
  }
  const parts = project.split('/')
  return parts[parts.length - 1] || project
}
