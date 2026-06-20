export type SupportedLocale = 'ru' | 'en'

export interface RouteMetaEntry {
  group: string
  label: string
}

export interface UiCatalog {
  common: {
    refresh: string
    loading: string
    none: string
    online: string
    offline: string
    readOnly: string
    liveBacked: string
    theme: {
      light: string
      dark: string
      auto: string
    }
    density: {
      comfortable: string
      compact: string
    }
  }
  app: {
    routeMeta: {
      home: RouteMetaEntry
      memories: RouteMetaEntry
      rules: RouteMetaEntry
      issues: RouteMetaEntry
      issueDetail: RouteMetaEntry
      projects: RouteMetaEntry
      vault: RouteMetaEntry
      tokens: RouteMetaEntry
      system: RouteMetaEntry
      settings: RouteMetaEntry
      admin: RouteMetaEntry
      fallback: RouteMetaEntry
    }
    globalSearchTitle: string
    globalSearchPlaceholder: string
    densityLabel: string
    themeTitle: string
    identityFallback: string
    identityRoleFallback: string
    reconnecting: string
    reconnectInSeconds: string
    updateAvailable: string
    updateAction: string
    updatingFallback: string
    updateApplied: string
    restart: string
    restarting: string
    updateFailed: string
    footerSessionsToday: string
    footerClients: string
    healthLabel: {
      healthy: string
      degraded: string
      unhealthy: string
      unknown: string
    }
  }
  sidebar: {
    sections: {
      workspace: string
      memory: string
      work: string
      storage: string
      admin: string
      service: string
    }
    items: {
      home: string
      memories: string
      rules: string
      issues: string
      projects: string
      vault: string
      tokens: string
      system: string
      settings: string
      admin: string
    }
    authDisabled: string
    connected: string
    disconnected: string
    logout: string
  }
  memories: {
    title: string
    subtitle: string
    projectSelect: string
    loading: string
    loadProjectsError: string
    loadMemoriesError: string
    noProjectsTitle: string
    noProjectsDescription: string
    emptyTitle: string
    emptyDescription: string
    listTitle: string
    listDescription: string
    detailTitle: string
    detailEmpty: string
    headers: {
      id: string
      content: string
      status: string
      scope: string
      citations: string
    }
    labels: {
      content: string
      status: string
      tier: string
      scope: string
      sourceAgent: string
      confidence: string
      injectionAccess: string
      tags: string
    }
    defaults: {
      active: string
      project: string
    }
  }
  rules: {
    title: string
    subtitle: string
    filterAll: string
    create: string
    loading: string
    loadError: string
    emptyTitle: string
    emptyDescription: string
    listTitle: string
    listDescription: string
    detailTitle: string
    detailEmpty: string
    reorderNote: string
    honestyNote: string
    immutableScopeNote: string
    headers: {
      scope: string
      rule: string
      priority: string
      version: string
    }
    labels: {
      scope: string
      priority: string
      version: string
      created: string
      updated: string
      editedBy: string
    }
    scope: {
      global: string
    }
    actions: {
      edit: string
      delete: string
      cancel: string
      save: string
      create: string
      creating: string
    }
    dialogs: {
      createTitle: string
      editTitle: string
      contentLabel: string
      scopeLabel: string
      contentPlaceholder: string
      deleteTitle: string
      deleteDescription: string
    }
  }
  issues: {
    title: string
    myIssues: string
    newIssue: string
    loading: string
    emptyTitle: string
    emptyDescription: string
    filters: {
      active: string
      open: string
      acknowledged: string
      resolved: string
      reopened: string
      closed: string
      rejected: string
      all: string
      bug: string
      feature: string
      improvement: string
      task: string
    }
    table: {
      priority: string
      type: string
      title: string
      project: string
      created: string
      messages: string
    }
    dialog: {
      title: string
      titleLabel: string
      titlePlaceholder: string
      descriptionLabel: string
      descriptionPlaceholder: string
      typeLabel: string
      priorityLabel: string
      targetProjectLabel: string
      selectType: string
      selectPriority: string
      noTarget: string
      cancel: string
      create: string
      creating: string
    }
    errors: {
      createFailed: string
      loadFailed: string
    }
  }
  vault: {
    title: string
    refresh: string
    statusTitle: string
    fields: {
      encryption: string
      keyFingerprint: string
      credentials: string
      project: string
      scope: string
      created: string
      actions: string
      name: string
    }
    encryptionEnabled: string
    encryptionDisabled: string
    enableHint: string
    notAvailable: string
    loading: string
    tryAgain: string
    emptyTitle: string
    emptyDescription: string
    reveal: string
    hide: string
    copy: string
    copied: string
    delete: string
    deleteTitle: string
    deleteDescription: string
    hidesIn: string
    errors: {
      loadFailed: string
      revealFailed: string
      deleteFailed: string
      decryptMismatch: string
    }
  }
  home: {
    productName: string
    loading: string
    systemHealth: string
    recentIssues: string
    viewAll: string
    noHealth: string
    noOpenIssues: string
    metrics: {
      sessionsToday: string
      connectedClients: string
      retrievalRequests: string
      contextInjections: string
      uptime: string
    }
  }
  issueDetail: {
    back: string
    loading: string
    invalidId: string
    loadFailed: string
    created: string
    by: string
    edit: string
    reject: string
    delete: string
    statusOverride: string
    addComment: string
    commentPlaceholder: string
    markdownHint: string
    send: string
    sending: string
    timeline: string
    save: string
    cancel: string
    deleteDialogTitle: string
    deleteDialogDescription: string
    rejectDialogTitle: string
    rejectDialogDescription: string
    rejectPlaceholder: string
    placeholders: {
      priority: string
      type: string
    }
    timelineTypes: {
      created: string
      acknowledged: string
      resolved: string
      reopened: string
      closed: string
      rejected: string
      comment: string
    }
  }
  projects: {
      title: string
      subtitle: string
      loading: string
      loadError: string
      emptyTitle: string
      emptyDescription: string
    tabs: {
      projects: string
      sessions: string
    }
    metrics: {
      projects: string
      sessionsWindow: string
      activeSessions: string
      totalServer: string
      activeStatus: string
    }
      notes: {
        windowSlice: string
        windowAll: string
        clientsTitle: string
        clientsCaption: string
        clientsDescription: string
        codeIntelTitle: string
        codeIntelCaption: string
        codeIntelDescription: string
      }
    registry: {
      title: string
      projectId: string
      sessionWindow: string
      active: string
      lastActivity: string
      liveRegistryId: string
    }
    sessions: {
      title: string
      filterAll: string
      countInFilter: string
      session: string
      project: string
      prompt: string
      state: string
      promptCount: string
      started: string
      outcomeMode: string
      promptMissing: string
      emptyTitle: string
      emptyDescription: string
    }
  }
  system: {
    title: string
    serverTitle: string
    healthTitle: string
    updatesTitle: string
    fields: {
      version: string
      uptime: string
      sessionsToday: string
      connectedClients: string
      retrievalRequests: string
      lastMaintenance: string
    }
    loadingHealth: string
    upToDate: string
    updateAvailable: string
    currentVersion: string
    updateNow: string
    updatingFallback: string
    updateApplied: string
    restart: string
    restarting: string
    updateFailed: string
  }
  settings: {
    title: string
    subtitle: string
    tabs: {
      general: string
      access: string
      server: string
    }
    general: {
      title: string
      description: string
      themeTitle: string
      themeDescription: string
      densityTitle: string
      densityDescription: string
      localeTitle: string
      localeDescription: string
      localeReadiness: string
      currentLocale: Record<SupportedLocale, string>
    }
    access: {
      title: string
      description: string
      authModeTitle: string
      authEnabled: string
      authDisabled: string
      currentUserTitle: string
      roleLabel: string
      adminLabel: string
      yes: string
      no: string
      followup: string
    }
    server: {
      title: string
      description: string
      configTitle: string
      configDescription: string
      configEmpty: string
      refresh: string
      readOnlyNote: string
    }
  }
}

export const messages: Record<SupportedLocale, UiCatalog> = {
  ru: {
    common: {
      refresh: 'Обновить',
      loading: 'Загрузка...',
      none: '—',
      online: 'онлайн',
      offline: 'оффлайн',
      readOnly: 'только чтение',
      liveBacked: 'live-backed',
      theme: {
        light: 'Светлая',
        dark: 'Тёмная',
        auto: 'Системная',
      },
      density: {
        comfortable: 'Комфорт',
        compact: 'Компактно',
      },
    },
    app: {
      routeMeta: {
        home: { group: 'Рабочее место', label: 'Сводка' },
        memories: { group: 'Память', label: 'Память' },
        rules: { group: 'Поведение и работа', label: 'Правила поведения' },
        issues: { group: 'Поведение и работа', label: 'Задачи' },
        issueDetail: { group: 'Поведение и работа', label: 'Задача' },
        projects: { group: 'Поведение и работа', label: 'Проекты и сессии' },
        vault: { group: 'Хранилище', label: 'Секреты' },
        tokens: { group: 'Хранилище', label: 'Токены' },
        system: { group: 'Администрирование', label: 'Состояние' },
        settings: { group: 'Администрирование', label: 'Настройки' },
        admin: { group: 'Сервис', label: 'Админ' },
        fallback: { group: 'Engram', label: 'Панель' },
      },
      globalSearchTitle: 'Глобальный поиск будет подключён отдельным live-backed срезом',
      globalSearchPlaceholder: 'Поиск по панели...',
      densityLabel: 'Плотность',
      themeTitle: 'Тема',
      identityFallback: 'оператор',
      identityRoleFallback: 'session',
      reconnecting: 'Переподключение',
      reconnectInSeconds: 'через {seconds}с',
      updateAvailable: 'Доступно обновление до',
      updateAction: 'Обновить',
      updatingFallback: 'Обновление...',
      updateApplied: 'Обновление применено',
      restart: 'Перезапустить',
      restarting: 'Перезапуск...',
      updateFailed: 'Обновление не удалось',
      footerSessionsToday: 'сессий сегодня',
      footerClients: 'клиентов',
      healthLabel: {
        healthy: 'в норме',
        degraded: 'деградация',
        unhealthy: 'сбой',
        unknown: 'нет данных',
      },
    },
    sidebar: {
      sections: {
        workspace: 'Рабочее место',
        memory: 'Память',
        work: 'Поведение и работа',
        storage: 'Хранилище',
        admin: 'Администрирование',
        service: 'Сервис',
      },
      items: {
        home: 'Сводка',
        memories: 'Память',
        rules: 'Правила поведения',
        issues: 'Задачи',
        projects: 'Проекты и сессии',
        vault: 'Секреты',
        tokens: 'Токены',
        system: 'Состояние',
        settings: 'Настройки',
        admin: 'Админ',
      },
      authDisabled: 'Auth disabled',
      connected: 'Подключено',
      disconnected: 'Отключено',
      logout: 'Выход',
    },
    memories: {
      title: 'Память',
      subtitle: 'Первый живой срез Memory Lab: список и деталь по текущему серверному surface.',
      projectSelect: 'Выберите проект',
      loading: 'Загружаю проекты и память...',
      loadProjectsError: 'Не удалось загрузить список проектов',
      loadMemoriesError: 'Не удалось загрузить память',
      noProjectsTitle: 'Нет доступных проектов',
      noProjectsDescription: 'Для Memory Lab нужен хотя бы один проект в текущем серверном реестре.',
      emptyTitle: 'Память пуста',
      emptyDescription: 'Для выбранного проекта пока нет сохранённых memory notes.',
      listTitle: 'Memory List',
      listDescription: 'Проект',
      detailTitle: 'Memory Detail',
      detailEmpty: 'Выберите запись слева, чтобы увидеть детали.',
      headers: {
        id: 'ID',
        content: 'Content',
        status: 'Status',
        scope: 'Scope',
        citations: 'Citations',
      },
      labels: {
        content: 'Content',
        status: 'Status',
        tier: 'Tier',
        scope: 'Scope',
        sourceAgent: 'Source agent',
        confidence: 'Confidence',
        injectionAccess: 'Injection / Access',
        tags: 'Tags',
      },
      defaults: {
        active: 'active',
        project: 'project',
      },
    },
    rules: {
      title: 'Правила поведения',
      subtitle: 'Первый живой slice для правил: list/create/edit/delete через текущий REST bridge без fake enable/disable.',
      filterAll: 'Все области',
      create: 'Новое правило',
      loading: 'Загружаю правила...',
      loadError: 'Не удалось загрузить правила',
      emptyTitle: 'Правил пока нет',
      emptyDescription: 'Текущий сервер не вернул ни одного активного правила для этого среза.',
      listTitle: 'Rule List',
      listDescription: 'Правила попадают в контекст новых сессий по server-side priority order.',
      detailTitle: 'Rule Detail',
      detailEmpty: 'Выберите правило слева, чтобы увидеть детали.',
      reorderNote: 'Порядок правил уже load-bearing, но drag-to-reorder в этом MVP ещё не выведен в живой экран.',
      honestyNote: 'Этот slice честно поддерживает только list/create/edit/delete. Включение/выключение и scope-change по месту ещё не live-backed.',
      immutableScopeNote: 'Scope пока неизменяем из edit path: если нужно поменять область, правило надо пересоздать.',
      headers: {
        scope: 'Область',
        rule: 'Правило',
        priority: 'Приоритет',
        version: 'Версия',
      },
      labels: {
        scope: 'Область',
        priority: 'Приоритет',
        version: 'Версия',
        created: 'Создано',
        updated: 'Обновлено',
        editedBy: 'Последний редактор',
      },
      scope: {
        global: 'глобально',
      },
      actions: {
        edit: 'Изменить',
        delete: 'Удалить',
        cancel: 'Отмена',
        save: 'Сохранить',
        create: 'Создать',
        creating: 'Создание...',
      },
      dialogs: {
        createTitle: 'Новое правило',
        editTitle: 'Изменить правило',
        contentLabel: 'Текст правила',
        scopeLabel: 'Область',
        contentPlaceholder: 'Сформулируйте правило для новых сессий...',
        deleteTitle: 'Удалить правило',
        deleteDescription: 'Правило будет мягко удалено. Это сейчас единственный честный способ деактивации.',
      },
    },
    issues: {
      title: 'Задачи',
      myIssues: 'Мои задачи',
      newIssue: 'Новая задача',
      loading: 'Загружаю задачи...',
      emptyTitle: 'Задач не найдено',
      emptyDescription: 'Задачи создаются агентами для связи между проектами. Этот экран показывает уже server-backed issue surface.',
      filters: {
        active: 'Активные',
        open: 'Открытые',
        acknowledged: 'Принятые',
        resolved: 'Решённые',
        reopened: 'Переоткрытые',
        closed: 'Закрытые',
        rejected: 'Отклонённые',
        all: 'Все',
        bug: 'Ошибка',
        feature: 'Возможность',
        improvement: 'Улучшение',
        task: 'Задача',
      },
      table: {
        priority: 'Приоритет',
        type: 'Тип',
        title: 'Заголовок',
        project: 'Проект',
        created: 'Создана',
        messages: 'Сообщ.',
      },
      dialog: {
        title: 'Новая задача',
        titleLabel: 'Заголовок',
        titlePlaceholder: 'Кратко опишите задачу',
        descriptionLabel: 'Описание',
        descriptionPlaceholder: 'Подробности задачи (необязательно)',
        typeLabel: 'Тип',
        priorityLabel: 'Приоритет',
        targetProjectLabel: 'Целевой проект',
        selectType: 'Выберите тип',
        selectPriority: 'Выберите приоритет',
        noTarget: '— нет —',
        cancel: 'Отмена',
        create: 'Создать задачу',
        creating: 'Создаю...',
      },
      errors: {
        createFailed: 'Не удалось создать задачу',
        loadFailed: 'Не удалось загрузить задачи',
      },
    },
    vault: {
      title: 'Секреты',
      refresh: 'Обновить',
      statusTitle: 'Состояние vault',
      fields: {
        encryption: 'Шифрование',
        keyFingerprint: 'Отпечаток ключа',
        credentials: 'Секреты',
        project: 'Проект',
        scope: 'Область',
        created: 'Создан',
        actions: 'Действия',
        name: 'Имя',
      },
      encryptionEnabled: 'Включено',
      encryptionDisabled: 'Отключено',
      enableHint: 'Для включения задайте env var `ENGRAM_VAULT_KEY`.',
      notAvailable: 'N/A',
      loading: 'Загружаю секреты...',
      tryAgain: 'Повторить',
      emptyTitle: 'Секретов пока нет',
      emptyDescription: 'Секреты появятся здесь после записи через server-backed vault surface.',
      reveal: 'Показать',
      hide: 'Скрыть',
      copy: 'Копировать',
      copied: 'Скопировано',
      delete: 'Удалить',
      deleteTitle: 'Удалить секрет',
      deleteDescription: 'Вы уверены, что хотите удалить этот секрет? Это действие необратимо.',
      hidesIn: 'Скроется через {seconds}с',
      errors: {
        loadFailed: 'Не удалось загрузить vault',
        revealFailed: 'Не удалось показать секрет',
        deleteFailed: 'Не удалось удалить секрет',
        decryptMismatch: 'Не удаётся расшифровать: этот секрет был зашифрован другим vault key. Задайте исходный `ENGRAM_VAULT_KEY`, чтобы показать его.',
      },
    },
    home: {
      productName: 'engram',
      loading: 'Загрузка...',
      systemHealth: 'Состояние системы',
      recentIssues: 'Последние задачи',
      viewAll: 'Смотреть все',
      noHealth: 'Компоненты health не были сообщены.',
      noOpenIssues: 'Открытых задач нет.',
      metrics: {
        sessionsToday: 'Сессий сегодня',
        connectedClients: 'Подключённых клиентов',
        retrievalRequests: 'Запросов retrieval',
        contextInjections: 'Инъекций контекста',
        uptime: 'Аптайм',
      },
    },
    issueDetail: {
      back: 'Назад к задачам',
      loading: 'Загружаю задачу...',
      invalidId: 'Некорректный ID задачи',
      loadFailed: 'Не удалось загрузить задачу',
      created: 'Создана',
      by: 'автор',
      edit: 'Изменить',
      reject: 'Отклонить',
      delete: 'Удалить',
      statusOverride: 'Принудительный статус:',
      addComment: 'Добавить комментарий (от оператора)',
      commentPlaceholder: 'Напишите комментарий... (`Ctrl+Enter` для отправки)',
      markdownHint: 'Поддерживается Markdown: `**bold**`, `*italic*`, `` `code` ``, code blocks и списки.',
      send: 'Отправить',
      sending: 'Отправка...',
      timeline: 'Лента',
      save: 'Сохранить',
      cancel: 'Отмена',
      deleteDialogTitle: 'Удалить задачу #{id}?',
      deleteDialogDescription: 'Это навсегда удалит задачу и все комментарии. Действие необратимо.',
      rejectDialogTitle: 'Отклонить задачу #{id}',
      rejectDialogDescription: 'Отклонённые задачи скрываются из всех агентских сессий. Укажите причину:',
      rejectPlaceholder: 'Причина отклонения (обязательно)...',
      placeholders: {
        priority: 'Приоритет',
        type: 'Тип',
      },
      timelineTypes: {
        created: 'создана',
        acknowledged: 'принята',
        resolved: 'решена',
        reopened: 'переоткрыта',
        closed: 'закрыта',
        rejected: 'отклонена',
        comment: 'комментарий',
      },
    },
    projects: {
      title: 'Проекты и сессии',
      subtitle: 'Первый живой срез по `.od`: текущий серверный реестр проектов и окно рабочих сессий без ложных must-build контролов.',
      loading: 'Загружаю проекты и сессии...',
      loadError: 'Не удалось загрузить проекты и сессии',
      emptyTitle: 'Проекты и сессии пока пусты',
      emptyDescription: 'Текущий сервер не вернул ни одного проекта и ни одной рабочей сессии.',
      tabs: {
        projects: 'Проекты',
        sessions: 'Сессии',
      },
      metrics: {
        projects: 'Проекты',
        sessionsWindow: 'Сессии в текущем окне',
        activeSessions: 'Активные сессии',
        totalServer: 'всего на сервере',
        activeStatus: 'по статусу `active`',
      },
      notes: {
        windowSlice: 'Показан честный срез: {shown} из {total} сессий из /api/sessions/list.',
        windowAll: 'Показаны все {shown} сессий, доступные через текущий list surface.',
        clientsTitle: 'Клиентские поверхности',
        clientsCaption: 'Следующий честный срез после проектов и сессий',
        clientsDescription: 'В `.od` этот блок существует как отдельная вкладка, но в текущем MVP он ещё не выведен из runtime substrate в живой экран.',
        codeIntelTitle: 'Индекс кода',
        codeIntelCaption: 'Flag-gated / следующая интеграция',
        codeIntelDescription: 'Code Intel останется отдельным truthful slice: его не стоит подмешивать сюда, пока не будет отдельного wiring и явной проверки флага.',
      },
      registry: {
        title: 'Project Registry',
        projectId: 'Проект',
        sessionWindow: 'Сессий в окне',
        active: 'Активных',
        lastActivity: 'Последняя активность',
        liveRegistryId: 'live-backed registry id',
      },
      sessions: {
        title: 'Session Window',
        filterAll: 'Все проекты',
        countInFilter: '{count} записей в текущем фильтре',
        session: 'Сессия',
        project: 'Проект',
        prompt: 'Задача',
        state: 'Состояние',
        promptCount: 'Prompt #',
        started: 'Начата',
        outcomeMode: 'результат {outcome} · режим {mode}',
        promptMissing: 'Промпт не сохранён или скрыт',
        emptyTitle: 'Сессии не найдены',
        emptyDescription: 'Для выбранного фильтра текущий server-side list surface не вернул записей.',
      },
    },
    system: {
      title: 'Состояние',
      serverTitle: 'Server',
      healthTitle: 'Health',
      updatesTitle: 'Updates',
      fields: {
        version: 'Version',
        uptime: 'Uptime',
        sessionsToday: 'Sessions today',
        connectedClients: 'Connected clients',
        retrievalRequests: 'Retrieval requests',
        lastMaintenance: 'Last maintenance',
      },
      loadingHealth: 'Загрузка...',
      upToDate: 'Сервер обновлён',
      updateAvailable: 'Доступно обновление',
      currentVersion: 'Текущая',
      updateNow: 'Обновить',
      updatingFallback: 'Обновление...',
      updateApplied: 'Обновление применено',
      restart: 'Перезапустить',
      restarting: 'Перезапуск...',
      updateFailed: 'Обновление не удалось',
    },
    settings: {
      title: 'Настройки',
      subtitle: 'Первый truthful settings slice: тема, плотность, доступ и текущее read-only состояние серверной конфигурации.',
      tabs: {
        general: 'Общие',
        access: 'Доступ',
        server: 'Сервер',
      },
      general: {
        title: 'Общие',
        description: 'Параметры рабочей консоли, которые уже честно поддержаны текущим runtime.',
        themeTitle: 'Тема',
        themeDescription: 'Управляет цветовым режимом Web UI.',
        densityTitle: 'Плотность интерфейса',
        densityDescription: 'Меняет отступы и плотность shell-поверхности.',
        localeTitle: 'Локаль интерфейса',
        localeDescription: 'i18n-слой заведён заранее, чтобы новые поверхности не хардкодили строки.',
        localeReadiness: 'Переключатель языка будет открыт после более широкой миграции существующих экранов на catalog-based строки.',
        currentLocale: {
          ru: 'Русский',
          en: 'English',
        },
      },
      access: {
        title: 'Доступ',
        description: 'Только truthful текущая auth/session правда без псевдо-админских контролов.',
        authModeTitle: 'Режим аутентификации',
        authEnabled: 'включена',
        authDisabled: 'отключена',
        currentUserTitle: 'Текущий пользователь',
        roleLabel: 'Роль',
        adminLabel: 'Админ-доступ',
        yes: 'да',
        no: 'нет',
        followup: 'Провайдеры входа, политика доступа и управление пользователями будут выводиться только как отдельные live-backed surfaces, а не как декоративные заглушки.',
      },
      server: {
        title: 'Сервер',
        description: 'Текущее read-only окно в `fetchConfig()` без притворства, что отсутствующие REST bridges уже готовы.',
        configTitle: 'Server Config',
        configDescription: 'Срез текущей серверной конфигурации, который реально отдает `/api/config`.',
        configEmpty: 'Сервер не вернул конфигурационные секции для этого среза.',
        refresh: 'Обновить конфигурацию',
        readOnlyNote: 'Редактирование model/access/runtime rows останется выключенным, пока для него не появятся честные live-backed endpoints.',
      },
    },
  },
  en: {
    common: {
      refresh: 'Refresh',
      loading: 'Loading...',
      none: '—',
      online: 'online',
      offline: 'offline',
      readOnly: 'read-only',
      liveBacked: 'live-backed',
      theme: {
        light: 'Light',
        dark: 'Dark',
        auto: 'System',
      },
      density: {
        comfortable: 'Comfortable',
        compact: 'Compact',
      },
    },
    app: {
      routeMeta: {
        home: { group: 'Workspace', label: 'Summary' },
        memories: { group: 'Memory', label: 'Memory' },
        rules: { group: 'Behavior & Work', label: 'Behavior Rules' },
        issues: { group: 'Behavior & Work', label: 'Issues' },
        issueDetail: { group: 'Behavior & Work', label: 'Issue' },
        projects: { group: 'Behavior & Work', label: 'Projects & Sessions' },
        vault: { group: 'Storage', label: 'Secrets' },
        tokens: { group: 'Storage', label: 'Tokens' },
        system: { group: 'Administration', label: 'System' },
        settings: { group: 'Administration', label: 'Settings' },
        admin: { group: 'Service', label: 'Admin' },
        fallback: { group: 'Engram', label: 'Console' },
      },
      globalSearchTitle: 'Global search will be connected as a separate live-backed slice',
      globalSearchPlaceholder: 'Search across the console...',
      densityLabel: 'Density',
      themeTitle: 'Theme',
      identityFallback: 'operator',
      identityRoleFallback: 'session',
      reconnecting: 'Reconnecting',
      reconnectInSeconds: 'in {seconds}s',
      updateAvailable: 'Update available to',
      updateAction: 'Update',
      updatingFallback: 'Updating...',
      updateApplied: 'Update applied',
      restart: 'Restart',
      restarting: 'Restarting...',
      updateFailed: 'Update failed',
      footerSessionsToday: 'sessions today',
      footerClients: 'clients',
      healthLabel: {
        healthy: 'healthy',
        degraded: 'degraded',
        unhealthy: 'failing',
        unknown: 'no data',
      },
    },
    sidebar: {
      sections: {
        workspace: 'Workspace',
        memory: 'Memory',
        work: 'Behavior & Work',
        storage: 'Storage',
        admin: 'Administration',
        service: 'Service',
      },
      items: {
        home: 'Summary',
        memories: 'Memory',
        rules: 'Behavior Rules',
        issues: 'Issues',
        projects: 'Projects & Sessions',
        vault: 'Secrets',
        tokens: 'Tokens',
        system: 'System',
        settings: 'Settings',
        admin: 'Admin',
      },
      authDisabled: 'Auth disabled',
      connected: 'Connected',
      disconnected: 'Disconnected',
      logout: 'Logout',
    },
    memories: {
      title: 'Memory',
      subtitle: 'First live Memory Lab slice: list and detail over the current server surface.',
      projectSelect: 'Choose project',
      loading: 'Loading projects and memory...',
      loadProjectsError: 'Failed to load project list',
      loadMemoriesError: 'Failed to load memory',
      noProjectsTitle: 'No projects available',
      noProjectsDescription: 'Memory Lab needs at least one project in the current server registry.',
      emptyTitle: 'Memory is empty',
      emptyDescription: 'There are no saved memory notes for the selected project yet.',
      listTitle: 'Memory List',
      listDescription: 'Project',
      detailTitle: 'Memory Detail',
      detailEmpty: 'Select a record on the left to inspect details.',
      headers: {
        id: 'ID',
        content: 'Content',
        status: 'Status',
        scope: 'Scope',
        citations: 'Citations',
      },
      labels: {
        content: 'Content',
        status: 'Status',
        tier: 'Tier',
        scope: 'Scope',
        sourceAgent: 'Source agent',
        confidence: 'Confidence',
        injectionAccess: 'Injection / Access',
        tags: 'Tags',
      },
      defaults: {
        active: 'active',
        project: 'project',
      },
    },
    rules: {
      title: 'Behavior Rules',
      subtitle: 'First live rules slice: list/create/edit/delete through the current REST bridge without fake enable/disable.',
      filterAll: 'All scopes',
      create: 'New rule',
      loading: 'Loading rules...',
      loadError: 'Failed to load rules',
      emptyTitle: 'No rules yet',
      emptyDescription: 'The current server returned no active rules for this slice.',
      listTitle: 'Rule List',
      listDescription: 'Rules enter new sessions according to the server-side priority order.',
      detailTitle: 'Rule Detail',
      detailEmpty: 'Select a rule on the left to inspect details.',
      reorderNote: 'Rule ordering is already load-bearing, but drag-to-reorder is not yet exposed as a live screen in this MVP.',
      honestyNote: 'This slice honestly supports only list/create/edit/delete. Enable/disable and in-place scope changes are not live-backed yet.',
      immutableScopeNote: 'Scope is immutable in the current edit path: recreate the rule if the scope must change.',
      headers: {
        scope: 'Scope',
        rule: 'Rule',
        priority: 'Priority',
        version: 'Version',
      },
      labels: {
        scope: 'Scope',
        priority: 'Priority',
        version: 'Version',
        created: 'Created',
        updated: 'Updated',
        editedBy: 'Last editor',
      },
      scope: {
        global: 'global',
      },
      actions: {
        edit: 'Edit',
        delete: 'Delete',
        cancel: 'Cancel',
        save: 'Save',
        create: 'Create',
        creating: 'Creating...',
      },
      dialogs: {
        createTitle: 'New rule',
        editTitle: 'Edit rule',
        contentLabel: 'Rule text',
        scopeLabel: 'Scope',
        contentPlaceholder: 'Write a rule for new sessions...',
        deleteTitle: 'Delete rule',
        deleteDescription: 'The rule will be soft-deleted. This is currently the only honest way to deactivate it.',
      },
    },
    issues: {
      title: 'Issues',
      myIssues: 'My Issues',
      newIssue: 'New Issue',
      loading: 'Loading issues...',
      emptyTitle: 'No issues found',
      emptyDescription: 'Issues are created by agents to communicate across projects. This screen shows the current server-backed issue surface.',
      filters: {
        active: 'Active',
        open: 'Open',
        acknowledged: 'Acknowledged',
        resolved: 'Resolved',
        reopened: 'Reopened',
        closed: 'Closed',
        rejected: 'Rejected',
        all: 'All',
        bug: 'Bug',
        feature: 'Feature',
        improvement: 'Improvement',
        task: 'Task',
      },
      table: {
        priority: 'Priority',
        type: 'Type',
        title: 'Title',
        project: 'Project',
        created: 'Created',
        messages: 'Msg',
      },
      dialog: {
        title: 'New Issue',
        titleLabel: 'Title',
        titlePlaceholder: 'Briefly describe the issue',
        descriptionLabel: 'Description',
        descriptionPlaceholder: 'Detailed description (optional)',
        typeLabel: 'Type',
        priorityLabel: 'Priority',
        targetProjectLabel: 'Target Project',
        selectType: 'Select type',
        selectPriority: 'Select priority',
        noTarget: '— none —',
        cancel: 'Cancel',
        create: 'Create Issue',
        creating: 'Creating...',
      },
      errors: {
        createFailed: 'Failed to create issue',
        loadFailed: 'Failed to load issues',
      },
    },
    vault: {
      title: 'Vault',
      refresh: 'Refresh',
      statusTitle: 'Vault Status',
      fields: {
        encryption: 'Encryption',
        keyFingerprint: 'Key Fingerprint',
        credentials: 'Credentials',
        project: 'Project',
        scope: 'Scope',
        created: 'Created',
        actions: 'Actions',
        name: 'Name',
      },
      encryptionEnabled: 'Enabled',
      encryptionDisabled: 'Disabled',
      enableHint: 'To enable, set env var `ENGRAM_VAULT_KEY`.',
      notAvailable: 'N/A',
      loading: 'Loading secrets...',
      tryAgain: 'Try again',
      emptyTitle: 'No credentials stored',
      emptyDescription: 'Credentials will appear here after being stored through the server-backed vault surface.',
      reveal: 'Reveal',
      hide: 'Hide',
      copy: 'Copy',
      copied: 'Copied!',
      delete: 'Delete',
      deleteTitle: 'Delete Credential',
      deleteDescription: 'Are you sure you want to delete this credential? This action cannot be undone.',
      hidesIn: 'Hides in {seconds}s',
      errors: {
        loadFailed: 'Failed to load vault',
        revealFailed: 'Failed to reveal credential',
        deleteFailed: 'Failed to delete credential',
        decryptMismatch: 'Cannot decrypt: this credential was encrypted with a different vault key. Set the original `ENGRAM_VAULT_KEY` to reveal it.',
      },
    },
    home: {
      productName: 'engram',
      loading: 'Loading...',
      systemHealth: 'System Health',
      recentIssues: 'Recent Issues',
      viewAll: 'View all',
      noHealth: 'No health components reported.',
      noOpenIssues: 'No open issues.',
      metrics: {
        sessionsToday: 'Sessions Today',
        connectedClients: 'Connected Clients',
        retrievalRequests: 'Retrieval Requests',
        contextInjections: 'Context Injections',
        uptime: 'Uptime',
      },
    },
    issueDetail: {
      back: 'Back to Issues',
      loading: 'Loading issue...',
      invalidId: 'Invalid issue ID',
      loadFailed: 'Failed to load issue',
      created: 'Created',
      by: 'by',
      edit: 'Edit',
      reject: 'Reject',
      delete: 'Delete',
      statusOverride: 'Status override:',
      addComment: 'Add Comment (as operator)',
      commentPlaceholder: 'Write a comment... (`Ctrl+Enter` to send)',
      markdownHint: 'Markdown supported: `**bold**`, `*italic*`, `` `code` ``, code blocks, and lists.',
      send: 'Send',
      sending: 'Sending...',
      timeline: 'Timeline',
      save: 'Save',
      cancel: 'Cancel',
      deleteDialogTitle: 'Delete Issue #{id}?',
      deleteDialogDescription: 'This permanently deletes the issue and all comments. Cannot be undone.',
      rejectDialogTitle: 'Reject Issue #{id}',
      rejectDialogDescription: 'Rejected issues are hidden from all agent sessions. Provide a reason:',
      rejectPlaceholder: 'Rejection reason (required)...',
      placeholders: {
        priority: 'Priority',
        type: 'Type',
      },
      timelineTypes: {
        created: 'created',
        acknowledged: 'acknowledged',
        resolved: 'resolved',
        reopened: 'reopened',
        closed: 'closed',
        rejected: 'rejected',
        comment: 'comment',
      },
    },
    projects: {
      title: 'Projects & Sessions',
      subtitle: 'First `.od`-aligned live slice: current server project registry and session window without fake must-build controls.',
      loading: 'Loading projects and sessions...',
      loadError: 'Failed to load projects and sessions',
      emptyTitle: 'Projects and sessions are empty',
      emptyDescription: 'The current server returned neither projects nor active work sessions.',
      tabs: {
        projects: 'Projects',
        sessions: 'Sessions',
      },
      metrics: {
        projects: 'Projects',
        sessionsWindow: 'Sessions in window',
        activeSessions: 'Active sessions',
        totalServer: 'total on server',
        activeStatus: 'by `active` status',
      },
      notes: {
        windowSlice: 'Showing an honest slice: {shown} of {total} sessions from /api/sessions/list.',
        windowAll: 'Showing all {shown} sessions available through the current list surface.',
        clientsTitle: 'Client surfaces',
        clientsCaption: 'Next truthful slice after projects and sessions',
        clientsDescription: 'This block exists as a separate tab in `.od`, but has not yet been lifted from the runtime substrate into a live screen in this MVP.',
        codeIntelTitle: 'Code index',
        codeIntelCaption: 'Flag-gated / next integration',
        codeIntelDescription: 'Code Intel stays a separate truthful slice: it should not be mixed in here before dedicated wiring and explicit flag verification exist.',
      },
      registry: {
        title: 'Project Registry',
        projectId: 'Project',
        sessionWindow: 'Sessions in window',
        active: 'Active',
        lastActivity: 'Last activity',
        liveRegistryId: 'live-backed registry id',
      },
      sessions: {
        title: 'Session Window',
        filterAll: 'All projects',
        countInFilter: '{count} records in the current filter',
        session: 'Session',
        project: 'Project',
        prompt: 'Task',
        state: 'State',
        promptCount: 'Prompt #',
        started: 'Started',
        outcomeMode: 'outcome {outcome} · mode {mode}',
        promptMissing: 'Prompt was not stored or was hidden',
        emptyTitle: 'No sessions found',
        emptyDescription: 'The current server-side list surface returned no records for this filter.',
      },
    },
    system: {
      title: 'System',
      serverTitle: 'Server',
      healthTitle: 'Health',
      updatesTitle: 'Updates',
      fields: {
        version: 'Version',
        uptime: 'Uptime',
        sessionsToday: 'Sessions today',
        connectedClients: 'Connected clients',
        retrievalRequests: 'Retrieval requests',
        lastMaintenance: 'Last maintenance',
      },
      loadingHealth: 'Loading...',
      upToDate: 'Server is up to date',
      updateAvailable: 'Update available',
      currentVersion: 'Current',
      updateNow: 'Update now',
      updatingFallback: 'Updating...',
      updateApplied: 'Update applied',
      restart: 'Restart',
      restarting: 'Restarting...',
      updateFailed: 'Update failed',
    },
    settings: {
      title: 'Settings',
      subtitle: 'First truthful settings slice: theme, density, access state, and the current read-only server config window.',
      tabs: {
        general: 'General',
        access: 'Access',
        server: 'Server',
      },
      general: {
        title: 'General',
        description: 'Console parameters already honestly supported by the current runtime.',
        themeTitle: 'Theme',
        themeDescription: 'Controls the Web UI color mode.',
        densityTitle: 'Interface density',
        densityDescription: 'Changes spacing and density across the shell surface.',
        localeTitle: 'Interface locale',
        localeDescription: 'The i18n layer is added up front so new surfaces stop hardcoding strings.',
        localeReadiness: 'The language switch will be exposed after a wider migration of existing screens to catalog-based strings.',
        currentLocale: {
          ru: 'Russian',
          en: 'English',
        },
      },
      access: {
        title: 'Access',
        description: 'Only truthful current auth/session state, without pseudo-admin controls.',
        authModeTitle: 'Authentication mode',
        authEnabled: 'enabled',
        authDisabled: 'disabled',
        currentUserTitle: 'Current user',
        roleLabel: 'Role',
        adminLabel: 'Admin access',
        yes: 'yes',
        no: 'no',
        followup: 'Login providers, access policy, and user management will only be exposed as separate live-backed surfaces, not decorative placeholders.',
      },
      server: {
        title: 'Server',
        description: 'Current read-only `fetchConfig()` window without pretending missing REST bridges already exist.',
        configTitle: 'Server Config',
        configDescription: 'The slice of server configuration that `/api/config` actually returns today.',
        configEmpty: 'The server did not return configuration sections for this slice.',
        refresh: 'Refresh config',
        readOnlyNote: 'Editing model/access/runtime rows stays disabled until honest live-backed endpoints exist for it.',
      },
    },
  },
}
