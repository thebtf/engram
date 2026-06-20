export type SurfaceTone = 'live' | 'must-build' | 'dormant' | 'stale'

export interface NavItem {
  label: string
  to: string
  tone?: SurfaceTone
  note?: string
}

export interface NavGroup {
  label: string
  items: NavItem[]
}

export const navGroups: NavGroup[] = [
  {
    label: 'Оператор',
    items: [
      { label: 'Сводка', to: '/', tone: 'live' },
      { label: 'Память', to: '/memories', tone: 'live' },
      { label: 'Правила', to: '/rules', tone: 'live' },
      { label: 'Проекты и сессии', to: '/projects', tone: 'live' },
      { label: 'Issues', to: '/issues', tone: 'live' },
      { label: 'Vault', to: '/vault', tone: 'live' },
      { label: 'Система', to: '/system', tone: 'live' },
      { label: 'Настройки', to: '/settings', tone: 'live' },
    ],
  },
  {
    label: 'Доступ',
    items: [
      {
        label: 'Access Admin',
        to: '/access',
        tone: 'must-build',
        note: 'multi-user surface',
      },
    ],
  },
  {
    label: 'Платформа',
    items: [
      {
        label: 'Platform Hub',
        to: '/platform',
        tone: 'must-build',
        note: 'separate module later',
      },
    ],
  },
]
