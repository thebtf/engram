export type SupportedLocale = 'ru' | 'en'

export interface RouteMetaEntry {
  group: string
  label: string
}

export interface UiCatalog {
  issueFlow: {
    issueHeading: string
    bulkTitle: string
    bulkHelp: string
    bulkClear: string
    bulkSelect: string
    bulkRunning: string
    bulkSubmit: string
    bulkDone: string
    bulkNeedOpen: string
    bulkSelectIssue: string
    bulkOnlyOpen: string
    loadingIssues: string
    noIssues: string
    backIssues: string
    editAction: string
    acknowledgeAction: string
    resolveAction: string
    reopenAction: string
    closeAction: string
    rejectAction: string
    deleteAction: string
    rejectHeading: string
    rejecting: string
    rejectSubmit: string
    deleteConfirm: string
    deleteHelp: string
    deleting: string
    deletePermanently: string
    noDescription: string
    metaHeading: string
    updatedLabel: string
    createdAt: string
    commentTotal: string
    selectForBulk: string
    sourceSession: string
    targetSession: string
    mutationSession: string
    sessionHelp: string
    sourceSessionFilter: string
    participantSessionFilter: string
    targetSessionFilter: string
    statusActive: string
    statusResolved: string
    statusClosed: string
    statusOpen: string
    statusAll: string
    issueRevision: string
    rejectReason: string
    rejectPlaceholder: string
    error_INVALID_ARGUMENT: string
    error_PAYLOAD_TOO_LARGE: string
    error_UNAUTHENTICATED: string
    error_FORBIDDEN: string
    error_ISSUE_NOT_FOUND: string
    error_ACTOR_CHANGED: string
    error_IDEMPOTENCY_CONFLICT: string
    error_REVISION_CONFLICT: string
    error_INVALID_TRANSITION: string
    error_REVISION_REQUIRED: string
    error_ISSUE_GONE: string
    error_DETAIL_PAGING_REQUIRED: string
    error_PRIVACY_REJECTED: string
    error_TRANSPORT_RECORD_TOO_LARGE: string
    error_UNAVAILABLE: string
    error_UNKNOWN_OUTCOME: string
    error_INVALID_RESPONSE: string
    emptyOffset: string
    reconcileDeleted: string
    reconcileCreate: string
    actorChangedResult: string
    resultUnavailable: string
    authChangedPending: string
    actorChangedStored: string
    intentRestored: string
    recoveryUnavailable: string
    actorChangedRevise: string
    rejectedNoWrites: string
    actorChangedKey: string
    deletedReconciled: string
    createRequired: string
    authChangedBefore: string
    actorRequired: string
    sourceChanged: string
    createInvalid: string
    confirmed: string
    commentsComplete: string
    commentsRemain: string
    historyComplete: string
    historyRemain: string
    unknownReference: string
    unknownActor: string
    revisionRequired: string
    titleRequired: string
    editInvalid: string
    statusInvalid: string
    revisionSelected: string
    readAccepted: string
    anotherAppend: string
    commentAdded: string
    rejectRequired: string
    fieldsUpdated: string
    commentOnlyAdded: string
    noChanges: string
    statusChanged: string
    intro: string
    humanLabels: string
    search: string
    ownerFilter: string
    recipientFilter: string
    issueType: string
    allTypes: string
    labelsDisclaimer: string
    readIssuesAgain: string
    previousIssues: string
    nextIssues: string
    restartList: string
    createPrivacy: string
    editRejected: string
    recoveryHeld: string
    checkRecovery: string
    labelsJson: string
    metadataJson: string
    ownerLabel: string
    recipientLabel: string
    createFieldsHelp: string
    readIssueAgain: string
    draftWarning: string
    inspectDraft: string
    readReconcile: string
    useRevision: string
    discardDraft: string
    submitStatus: string
    commentUnknown: string
    readComments: string
    allowAppend: string
    statusComment: string
    statusMetadata: string
    statusHelp: string
    returnDraft: string
    descriptionHelp: string
    editMetadataHelp: string
    editOwnerHelp: string
    editComment: string
    editCommentMetadata: string
    editCommitHelp: string
    actor: string
    labels: string
    viewMetadata: string
    commentMetadata: string
    commentShortcut: string
    emptyComments: string
    oversizedComment: string
    viewOversizedComment: string
    viewCommentMetadata: string
    previousComments: string
    nextComments: string
    history: string
    oversizedHistory: string
    emptyHistory: string
    viewHistory: string
    authorizedValues: string
    previousHistory: string
    nextHistory: string
    pagingHelp: string
    authorizedRows: string
    revisionCard: string
    ownerRecipient: string
    unassigned: string
    retainedIntent: string
    outcome_pending: string
    outcome_rejected: string
    outcome_confirmed: string
    retainedDraft: string
    retainedSource: string
    retainedStatus: string
    editBase: string
    draftSource: string
    commentCount: string
    historyCount: string
    title: string
    description: string
    type: string
    priority: string
    sourceContext: string
    newIssue: string
    targetProject: string
    selectTarget: string
    value_task: string
    value_bug: string
    value_feature: string
    value_improvement: string
    value_medium: string
    value_low: string
    value_high: string
    value_critical: string
    creating: string
    create: string
    sourceProject: string
    chooseSource: string
    ownerRecipientLabel: string
    saveEdit: string
    saving: string
    cancel: string
    comment: string
    addComment: string
    sending: string
    sendComment: string
    writeComment: string
    unknown: string
    unknownAgent: string
    noneRecorded: string
    previousRevision: string
    historyActor: string
    changedFields: string
    commentActor: string
    historyRevision: string
    historyActionCreated: string
    historyActionUpdated: string
    comments: string
  }
  projectCatalog: {
    unresolvedTitle: string
    unresolvedReason: string
    review: string
    kind: string
    memory: string
    document: string
    issue: string
    code: string
    metadataOnly: string
    readerKindsHelp: string
    history: string
    more: string
    back: string
    unknownProject: string
    ambiguousName: string
    selectionRequired: string
    loading: string
    search: string
  }
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
    projectAndGlobal: string
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
    issueFlow: {
      issueHeading: "Задачи",
      bulkTitle: "Массовое подтверждение",
      bulkHelp: "Подтвердить можно только открытые задачи.",
      bulkClear: "Снять выбор открытых задач",
      bulkSelect: "Выбрать открытые ({count})",
      bulkRunning: "Подтверждение...",
      bulkSubmit: "Подтвердить выбранные ({count})",
      bulkDone: "Подтверждено открытых задач: {count}.",
      bulkNeedOpen: "Выберите открытые задачи для подтверждения.",
      bulkSelectIssue: "Выбрать задачу {id} для массового подтверждения",
      bulkOnlyOpen: "Массовое подтверждение доступно только для открытых задач",
      loadingIssues: "Загрузка задач...",
      noIssues: "Нет задач по выбранным фильтрам.",
      backIssues: "К списку задач",
      editAction: "Изменить",
      acknowledgeAction: "Подтвердить",
      resolveAction: "Отметить выполненной",
      reopenAction: "Открыть снова",
      closeAction: "Закрыть",
      rejectAction: "Отклонить",
      deleteAction: "Удалить",
      rejectHeading: "Отклонить задачу",
      rejecting: "Отклонение...",
      rejectSubmit: "Отклонить с причиной",
      deleteConfirm: "Удалить задачу №{id} без возможности восстановления?",
      deleteHelp: "Задача и её комментарии будут удалены без возможности восстановления.",
      deleting: "Удаление...",
      deletePermanently: "Удалить без восстановления",
      noDescription: "Нет описания",
      metaHeading: "Сведения",
      updatedLabel: "Обновлено",
      createdAt: "создано {date}",
      commentTotal: "Комментариев: {count}",
      selectForBulk: "Выбрать для массового подтверждения",
      sourceSession: 'Сессия создания (необязательно)',
      targetSession: 'Сессия получателя (необязательно)',
      mutationSession: 'Сессия изменения (необязательно)',
      sessionHelp: 'Сессия указывает происхождение или получателя и не даёт разрешений.',
      sourceSessionFilter: 'Фильтр по сессии создания',
      participantSessionFilter: 'Фильтр по сессии участника',
      targetSessionFilter: 'Фильтр по сессии получателя',
      statusActive: "Активные",
      statusResolved: "Выполнено · ожидает проверки",
      statusClosed: "Закрыто",
      statusOpen: "Открытые",
      statusAll: "Все",
      issueRevision: "задача №{id} · ревизия {revision}",
      rejectReason: "Причина отклонения",
      rejectPlaceholder: "Укажите причину. Она будет сохранена как комментарий.",
      error_INVALID_ARGUMENT: "Некорректное поле задачи, метаданные, число или параметр страницы. Исправьте данные перед отправкой.",
      error_PAYLOAD_TOO_LARGE: "Запрос задачи превышает допустимый предел. Данные не были обрезаны.",
      error_UNAUTHENTICATED: "Пройдите аутентификацию перед просмотром или изменением задачи.",
      error_FORBIDDEN: "Текущему субъекту не разрешено это действие с задачей.",
      error_ISSUE_NOT_FOUND: "Задача отсутствует или недоступна текущему субъекту.",
      error_ACTOR_CHANGED: "Сохранённое ожидание субъекта не совпало. Сохраните исходные субъект, ключ, источник и данные запроса и выполните явную сверку перед новой отправкой.",
      error_IDEMPOTENCY_CONFLICT: "Сверьте исходный запрос создания с исходными субъектом, источником, ключом и данными. Не создавайте замену.",
      error_REVISION_CONFLICT: "Сохраните исходное состояние и черновик. Загрузите текущую задачу и выполните явную сверку перед повторной отправкой.",
      error_INVALID_TRANSITION: "Загрузите текущую задачу и выберите разрешённый переход.",
      error_REVISION_REQUIRED: "Сохраните черновик и явно сверьте его с загруженной ревизией перед отправкой существенного изменения.",
      error_ISSUE_GONE: "Исходная задача удалена. Её сохранённый запрос не должен создавать замену.",
      error_DETAIL_PAGING_REQUIRED: "Загрузите detail_mode=paged и явно переходите по продолжению каждой коллекции.",
      error_PRIVACY_REJECTED: "Запись задачи отклонена действующей политикой конфиденциальности. Данные запроса не раскрывались и не скрывались без уведомления.",
      error_TRANSPORT_RECORD_TOO_LARGE: "Загрузите необрезанную запись через доступную по разрешениям постраничную выдачу REST.",
      error_UNAVAILABLE: "Результат неизвестен. Сохраните исходный запрос или исходное состояние и черновик; выполните явную сверку после восстановления.",
      error_UNKNOWN_OUTCOME: "Результат неизвестен. Сохраните исходный запрос или исходное состояние и черновик; выполните явную сверку перед новой записью.",
      error_INVALID_RESPONSE: "Не удалось подтвердить ответ по задаче. Сохраните исходный запрос или исходное состояние и черновик; выполните явную сверку.",
      emptyOffset: "Пустая страница, смещение: {offset}",
      reconcileDeleted: "Завершить сверку: исходная задача удалена",
      reconcileCreate: "Сверить исходный запрос на создание",
      actorChangedResult: "ACTOR_CHANGED: Результат сохранён. Восстановите исходные данные субъекта и проекта-источника, чтобы завершить сверку.",
      resultUnavailable: "Результат создания сохранён, но хранилище восстановления или проверка субъекта недоступны. Не создавайте другую задачу.",
      authChangedPending: "ACTOR_CHANGED: Аутентификация изменилась во время ожидания восстановления. Снова проверьте исходного субъекта и проект-источник; повторный запрос не отправлялся.",
      actorChangedStored: "ACTOR_CHANGED: Сохранённый запрос относится к другому субъекту или каноническому проекту-источнику. Восстановите этот контекст; повторный запрос или замена не отправлялись.",
      intentRestored: "Исходный запрос восстановлен из этой вкладки браузера. Он не был отправлен повторно.",
      recoveryUnavailable: "Восстановление недоступно. Сохранённый запрос не был перезаписан или отправлен. Восстановите хранилище браузера и исходного субъекта и проект-источник перед продолжением.",
      actorChangedRevise: "ACTOR_CHANGED: Восстановите исходного субъекта перед изменением отклонённого запроса. Ничего не отправлено.",
      rejectedNoWrites: "Отклонённый запрос не изменил данные. Проверьте сохранённую форму; следующее явное создание начнёт новый запрос.",
      actorChangedKey: "ACTOR_CHANGED: Восстановите исходного субъекта. Исходные ключ и данные запроса сохранены; ничего не отправлено.",
      deletedReconciled: "Исходная задача удалена. Сверка завершена явно; замена не создана.",
      createRequired: "Выберите канонический проект-источник, укажите заголовок и выберите целевой проект.",
      authChangedBefore: "ACTOR_CHANGED: Аутентификация изменилась перед отправкой. Ничего не отправлено.",
      actorRequired: "UNAUTHENTICATED: Требуется подтверждённый субъект задачи. Ничего не отправлено.",
      sourceChanged: "Проект-источник изменился перед отправкой. Ничего не отправлено.",
      createInvalid: "INVALID_ARGUMENT: Проверьте поля JSON, канонические проекты и хранилище восстановления браузера. Ничего не отправлено.",
      confirmed: "Задача №{id} подтверждена. Для намеренно созданной новой задачи будет использован новый ключ.",
      commentsComplete: "последующих комментариев нет",
      commentsRemain: "есть последующие комментарии",
      historyComplete: "последующих записей истории нет",
      historyRemain: "есть последующие записи истории",
      unknownReference: "неизвестная ссылка",
      unknownActor: "Неизвестный субъект (исторические данные об авторстве недоступны)",
      revisionRequired: "REVISION_REQUIRED: Перед отправкой существенного изменения загрузите корректную десятичную ревизию.",
      titleRequired: "INVALID_ARGUMENT: Заголовок не может быть пустым.",
      editInvalid: "INVALID_ARGUMENT: Введите метаданные как объекты JSON, а метки — как массив строк JSON. Для метаданных комментария нужен непустой комментарий. Ничего не отправлено.",
      statusInvalid: "INVALID_ARGUMENT: Метаданные комментария к статусу должны быть объектом JSON, а комментарий — непустым. Ничего не отправлено.",
      revisionSelected: "Текущая ревизия выбрана явно. Проверьте сохранённый черновик; ничего не отправлено повторно.",
      readAccepted: "Текущее состояние принято явно. Сохранённое действие отброшено без повторной отправки.",
      anotherAppend: "Повторная отправка добавит ещё один комментарий; дубликаты не удаляются.",
      commentAdded: "Комментарий добавлен. Повторная отправка добавит ещё один комментарий.",
      rejectRequired: "INVALID_ARGUMENT: Для отклонения нужна причина; она сохраняется как комментарий.",
      fieldsUpdated: "Поля задачи обновлены.",
      commentOnlyAdded: "Комментарий добавлен без изменения ревизии задачи.",
      noChanges: "Существенные изменения не потребовались.",
      statusChanged: "Статус задачи изменён на {status}.",
      intro: "Находите, продолжайте и обновляйте задачи со сверкой конфликтов и восстановлением создания.",
      humanLabels: "метки источника human",
      search: "Поиск по заголовку или описанию",
      ownerFilter: "Фильтр по метке владельца",
      recipientFilter: "Фильтр по метке получателя",
      issueType: "Тип задачи",
      allTypes: "Все типы",
      labelsDisclaimer: "Агент-источник, владелец и получатель — это метки, а не подтверждённые личности или разрешения. Страницы обновляются в реальном времени и не являются снимком; после параллельных изменений начните просмотр заново.",
      readIssuesAgain: "Загрузить задачи снова",
      previousIssues: "Предыдущая страница задач",
      nextIssues: "Следующая страница задач",
      restartList: "Начать просмотр списка заново",
      createPrivacy: "Перед отправкой эта вкладка сохраняет исходные данные запроса и не содержащую секретов привязку субъекта и источника для восстановления после обновления. Не вводите учётные данные или секреты из хранилища. Автоматического повтора или создания замены не будет.",
      editRejected: "Изменить отклонённый черновик после отказа без записи",
      recoveryHeld: "Восстановление приостановлено. Восстановите исходного аутентифицированного субъекта, канонический проект-источник и хранилище этой вкладки. Автоматического повтора или замены не будет.",
      checkRecovery: "Проверить контекст восстановления снова",
      labelsJson: "Метки (массив JSON)",
      metadataJson: "Метаданные (объект JSON)",
      ownerLabel: "Метка владельца",
      recipientLabel: "Метка получателя",
      createFieldsHelp: "Пустой владелец или получатель означает отсутствие назначения (null). Метаданные должны быть объектом; {'{}'} — пустой объект, а не null.",
      readIssueAgain: "Загрузить текущую задачу снова",
      draftWarning: "Автоматического повтора или переноса на новую ревизию не будет. Отсутствие ответа может означать, что действие выполнено. Загрузите текущую задачу, сравните исходное состояние и черновик, затем явно выберите, отправлять ли запрос снова.",
      inspectDraft: "Посмотреть исходное состояние и отправленный черновик",
      readReconcile: "Загрузить текущую задачу для сверки",
      useRevision: "Использовать загруженную ревизию для сохранённого черновика (без отправки)",
      discardDraft: "Принять загруженное состояние и отбросить сохранённое действие",
      submitStatus: "Отправить сверенное изменение статуса",
      commentUnknown: "Результат отправки комментария неизвестен. Черновик сохранён. Комментарии только добавляются, дубликаты не удаляются: проверьте страницы комментариев перед повторной отправкой.",
      readComments: "Загрузить текущие комментарии для сверки",
      allowAppend: "Комментарии проверены; разрешить повторное добавление",
      statusComment: "Комментарий к результату изменения статуса (необязательно)",
      statusMetadata: "Метаданные комментария к статусу (объект JSON)",
      statusHelp: "Следующее изменение статуса отправит этот необязательный результат вместе с загруженной ревизией в одной транзакции. Для отклонения требуется отдельная причина.",
      returnDraft: "Вернуться к исходной карточке для сверки черновика",
      descriptionHelp: "Пустое описание очищает текст задачи. Неизменённые поля не включаются в запрос на изменение.",
      editMetadataHelp: "Неизменённые метаданные сохраняются. {'{}'} явно заменяет весь объект; null не допускается.",
      editOwnerHelp: "Пустой владелец или получатель снимает назначение с помощью null. Метки не предоставляют разрешений.",
      editComment: "Комментарий к изменению (необязательно)",
      editCommentMetadata: "Метаданные комментария к изменению (объект JSON)",
      editCommitHelp: "Изменение и необязательный комментарий сохраняются вместе либо не сохраняются вовсе. Повторная отправка только комментария не устраняет дубликаты.",
      actor: "Субъект",
      labels: "Метки",
      viewMetadata: "Посмотреть все метаданные задачи",
      commentMetadata: "Метаданные комментария (объект JSON)",
      commentShortcut: "Ctrl/⌘+Enter добавляет комментарий. Повторная отправка не устраняет дубликаты.",
      emptyComments: "На этой странице нет комментариев. Список обновляется в реальном времени; на предыдущих страницах комментарии могут быть.",
      oversizedComment: "Необрезанный комментарий превышает целевой размер страницы 4 МиБ.",
      viewOversizedComment: "Посмотреть полную запись большого комментария",
      viewCommentMetadata: "Посмотреть метаданные комментария",
      previousComments: "Предыдущая страница комментариев",
      nextComments: "Следующая страница комментариев",
      history: "История",
      oversizedHistory: "Необрезанная запись истории превышает целевой размер страницы 4 МиБ.",
      emptyHistory: "На этой странице нет записей истории.",
      viewHistory: "Посмотреть все значения записи истории",
      authorizedValues: "Показаны только возвращённые значения, доступные по разрешениям. Отсутствующие исторические значения неизвестны.",
      previousHistory: "Предыдущая страница истории",
      nextHistory: "Следующая страница истории",
      pagingHelp: "Страницы обновляются в реальном времени и не являются снимком. Параллельные записи могут перемещать элементы. Страницы не загружаются автоматически.",
      authorizedRows: "{range} · доступных записей: {count}",
      revisionCard: "Ревизия {revision} · откройте карточку, чтобы увидеть полное описание и метаданные.",
      ownerRecipient: "Владелец: {owner} · получатель: {recipient}",
      unassigned: "Не назначен",
      retainedIntent: "Исходный запрос сохранён ({outcome}). Редактирование не изменит его данные или ключ.",
      outcome_pending: "ожидает ответа",
      outcome_rejected: "отклонён",
      outcome_confirmed: "подтверждён",
      retainedDraft: "Сохранённый черновик задачи №{id} · исходная ревизия {revision}",
      retainedSource: "Источник сохранённого действия: {source}. Повторная загрузка не изменяет этот источник или исходное состояние.",
      retainedStatus: "Сохранённое действие для явно выбранной ревизии {revision}: {fields}. Причина может быть добавлена повторно, если предыдущее действие выполнено.",
      editBase: "Изменение полей · исходная ревизия {revision}",
      draftSource: "Черновик задачи №{id} · источник действия: {source}.",
      commentCount: "{range} · доступных комментариев: {count} · {completion}",
      historyCount: "{range} · доступных записей истории: {count} · {completion}. Завершение относится к этому смещению и не подтверждает загрузку предыдущих страниц.",
      title: "Заголовок",
      description: "Описание",
      type: "Тип",
      priority: "Приоритет",
      sourceContext: "Контекст проекта (источник действий человека)",
      newIssue: "Новая задача",
      targetProject: "Целевой проект",
      selectTarget: "выберите целевой проект",
      value_task: "Задача",
      value_bug: "Ошибка",
      value_feature: "Возможность",
      value_improvement: "Улучшение",
      value_medium: "Средний",
      value_low: "Низкий",
      value_high: "Высокий",
      value_critical: "Критический",
      creating: "Создание...",
      create: "Создать задачу",
      sourceProject: "Проект-источник",
      chooseSource: "Выберите проект-источник",
      ownerRecipientLabel: "Метки владельца и получателя",
      saveEdit: "Сохранить изменения",
      saving: "Сохранение...",
      cancel: "Отмена",
      comment: "Комментарий",
      addComment: "Добавить комментарий",
      sending: "Отправка...",
      sendComment: "Отправить комментарий",
      writeComment: "Напишите комментарий оператора...",
      unknown: "неизвестно",
      unknownAgent: "неизвестный агент",
      noneRecorded: "не указаны",
      previousRevision: "Предыдущая ревизия: {revision} · {date}",
      historyActor: "Субъект: {actor} · источник: {source} · метка агента: {agent} · атрибуция сессии: {session}",
      changedFields: "Изменённые поля: {fields}",
      commentActor: "Субъект: {actor}",
      historyRevision: "Ревизия {revision} · {action}",
      historyActionCreated: "создана",
      historyActionUpdated: "обновлена",
      comments: "Комментарии",
    },
    projectCatalog: {
      unresolvedTitle: 'Неразобранные записи', unresolvedReason: 'Причина неопределённости', review: 'Разобрать сохранённые записи',
      kind: 'Вид записей', memory: 'Память', document: 'Документы', issue: 'Задачи', history: 'История', more: 'Следующая страница', back: 'Вернуться',
      code: 'Код', metadataOnly: 'Сохранена метаинформация группы. Для её записей нет reader в этом обзоре; это не означает отсутствие исходных данных.',
      readerKindsHelp: 'Виды чтения указаны по сохранённой принадлежности. Доступность, целостность и права проверяются при чтении.',
      unknownProject: 'Проект недоступен или ещё не определён.', ambiguousName: 'Одинаковое имя; принадлежность не определена по имени.',
      selectionRequired: 'Выбери проект из каталога, прежде чем продолжить.', loading: 'Загрузка каталога и определение проекта…', search: 'Поиск',
    },
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
      filterAll: 'Только глобальные правила',
      projectAndGlobal: 'Правила выбранного проекта и глобальные правила',
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
    issueFlow: {
      issueHeading: "Issues",
      bulkTitle: "Bulk acknowledge",
      bulkHelp: "Only open issues can be acknowledged.",
      bulkClear: "Clear open selection",
      bulkSelect: "Select open ({count})",
      bulkRunning: "Acknowledging...",
      bulkSubmit: "Acknowledge selected ({count})",
      bulkDone: "Acknowledged {count} open issue(s).",
      bulkNeedOpen: "Select open issues before acknowledging.",
      bulkSelectIssue: "Select issue {id} for bulk acknowledge",
      bulkOnlyOpen: "Bulk acknowledge only applies to open issues",
      loadingIssues: "Loading issues...",
      noIssues: "No issues match the current filters.",
      backIssues: "Back to issues",
      editAction: "Edit",
      acknowledgeAction: "Acknowledge",
      resolveAction: "Mark resolved",
      reopenAction: "Reopen",
      closeAction: "Close",
      rejectAction: "Reject",
      deleteAction: "Delete",
      rejectHeading: "Reject issue",
      rejecting: "Rejecting...",
      rejectSubmit: "Reject with reason",
      deleteConfirm: "Delete issue #{id} permanently?",
      deleteHelp: "This permanently removes the issue and its comments.",
      deleting: "Deleting...",
      deletePermanently: "Delete permanently",
      noDescription: "No description",
      metaHeading: "Details",
      updatedLabel: "Updated",
      createdAt: "created {date}",
      commentTotal: "{count} comments",
      selectForBulk: "Select for bulk acknowledge",
      sourceSession: 'Creation session (optional)',
      targetSession: 'Recipient session (optional)',
      mutationSession: 'Change session (optional)',
      sessionHelp: 'Session references record provenance or a recipient; they do not grant permissions.',
      sourceSessionFilter: 'Creation session filter',
      participantSessionFilter: 'Participant session filter',
      targetSessionFilter: 'Recipient session filter',
      statusActive: "Active",
      statusResolved: "Done · awaiting verification",
      statusClosed: "Closed",
      statusOpen: "Open",
      statusAll: "All",
      issueRevision: "issue #{id} · revision {revision}",
      rejectReason: "Rejection reason",
      rejectPlaceholder: "Reason is required and will be stored as a comment.",
      error_INVALID_ARGUMENT: "Invalid issue field, metadata, number or paging argument. Correct the input before submitting.",
      error_PAYLOAD_TOO_LARGE: "The issue request exceeds an admitted limit. Nothing was truncated.",
      error_UNAUTHENTICATED: "Authenticate before reading or acting on an issue.",
      error_FORBIDDEN: "The current actor is not allowed to perform this issue action.",
      error_ISSUE_NOT_FOUND: "The issue is missing or unavailable to the current actor.",
      error_ACTOR_CHANGED: "The saved actor expectation did not match. Preserve the original actor, key, source and payload and reconcile explicitly before any new dispatch.",
      error_IDEMPOTENCY_CONFLICT: "Reconcile the original create intent with its original actor, source, key and payload. Do not create a replacement.",
      error_REVISION_CONFLICT: "Keep the original base and draft. Read the current issue and explicitly reconcile before submitting again.",
      error_INVALID_TRANSITION: "Read the current issue and choose a permitted transition.",
      error_REVISION_REQUIRED: "Keep the draft and explicitly reconcile it with a read revision before submitting a material change.",
      error_ISSUE_GONE: "The original issue was deleted. Its retained intent must not create a replacement.",
      error_DETAIL_PAGING_REQUIRED: "Read detail_mode=paged and follow each collection continuation explicitly.",
      error_PRIVACY_REJECTED: "The issue write was rejected by the applicable privacy policy. No payload was disclosed or silently redacted.",
      error_TRANSPORT_RECORD_TOO_LARGE: "Read the intact record through authorized REST detail paging.",
      error_UNAVAILABLE: "Outcome unknown. Retain the original intent or base and draft; reconcile explicitly after recovery.",
      error_UNKNOWN_OUTCOME: "Outcome unknown. Retain the original intent or base and draft; reconcile explicitly before any new write.",
      error_INVALID_RESPONSE: "The issue response could not be confirmed. Retain the original intent or base and draft; reconcile explicitly.",
      emptyOffset: "Empty page at offset {offset}",
      reconcileDeleted: "Finish reconciliation: original issue deleted",
      reconcileCreate: "Reconcile original create intent",
      actorChangedResult: "ACTOR_CHANGED: The outcome is retained. Restore the original actor and source to finish reconciliation.",
      resultUnavailable: "The create result is retained, but recovery storage or actor verification is unavailable. Do not start another task.",
      authChangedPending: "ACTOR_CHANGED: Authentication changed while recovery was pending. Verify the original actor and source again; nothing was replayed.",
      actorChangedStored: "ACTOR_CHANGED: A pending intent is retained for another actor or canonical source. Restore that context; no replay or replacement was sent.",
      intentRestored: "Original intent restored from this browser tab. It has not been submitted again.",
      recoveryUnavailable: "Recovery is unavailable. The stored intent was not overwritten or sent. Restore browser storage and the original actor/source before continuing.",
      actorChangedRevise: "ACTOR_CHANGED: Restore the original actor before revising the rejected intent. Nothing was sent.",
      rejectedNoWrites: "The rejected intent had no writes. Review the retained form; the next explicit create starts a new intent.",
      actorChangedKey: "ACTOR_CHANGED: Restore the original actor. The original key and payload remain stored; nothing was sent.",
      deletedReconciled: "The original issue was deleted. Reconciliation finished explicitly; no replacement was created.",
      createRequired: "Choose a canonical source project, title and target project.",
      authChangedBefore: "ACTOR_CHANGED: Authentication changed before submission. Nothing was sent.",
      actorRequired: "UNAUTHENTICATED: A verified issue actor is required. Nothing was sent.",
      sourceChanged: "Source changed before submission. Nothing was sent.",
      createInvalid: "INVALID_ARGUMENT: Check the JSON fields, canonical projects and browser recovery storage. Nothing was sent.",
      confirmed: "Issue #{id} confirmed. A deliberately new task will use a new key.",
      commentsComplete: "no later comments",
      commentsRemain: "later comments remain",
      historyComplete: "no later history",
      historyRemain: "later history remains",
      unknownReference: "unknown reference",
      unknownActor: "Unknown actor (historical attribution unavailable)",
      revisionRequired: "REVISION_REQUIRED: Read a valid decimal revision before submitting a material change.",
      titleRequired: "INVALID_ARGUMENT: Title cannot be empty.",
      editInvalid: "INVALID_ARGUMENT: Enter metadata as JSON objects and labels as a JSON array of strings. Comment metadata requires a nonempty comment. Nothing was sent.",
      statusInvalid: "INVALID_ARGUMENT: Status comment metadata must be a JSON object with a nonempty comment. Nothing was sent.",
      revisionSelected: "Current revision selected explicitly. Review the retained draft; nothing was resubmitted.",
      readAccepted: "Current read accepted explicitly. The retained action was discarded without resubmission.",
      anotherAppend: "Another send will append another comment; comments are not deduplicated.",
      commentAdded: "Comment added. A repeated send would append another comment.",
      rejectRequired: "INVALID_ARGUMENT: Reject requires a reason stored as a comment.",
      fieldsUpdated: "Issue fields updated.",
      commentOnlyAdded: "Comment appended without changing the issue revision.",
      noChanges: "No material changes were needed.",
      statusChanged: "Issue status changed to {status}.",
      intro: "Find, resume and update issue-backed tasks with explicit conflict and create recovery.",
      humanLabels: "human source labels",
      search: "Search title or body",
      ownerFilter: "Owner label filter",
      recipientFilter: "Recipient label filter",
      issueType: "Issue type",
      allTypes: "All types",
      labelsDisclaimer: "Source-agent, owner and recipient are labels, not authenticated identities or permissions. Live paging is not a snapshot; restart after concurrent changes.",
      readIssuesAgain: "Read issues again",
      previousIssues: "Previous issues page",
      nextIssues: "Next issues page",
      restartList: "Restart live list",
      createPrivacy: "Before sending, this tab stores the original payload and non-secret actor/source binding for refresh recovery. Do not enter credentials or vault material. No automatic retry or replacement task.",
      editRejected: "Edit rejected draft after no-write refusal",
      recoveryHeld: "Recovery is held. Restore the original authenticated actor, canonical source and this tab's storage. Nothing will be replayed or replaced automatically.",
      checkRecovery: "Check recovery context again",
      labelsJson: "Labels (JSON array)",
      metadataJson: "Metadata (JSON object)",
      ownerLabel: "Owner label",
      recipientLabel: "Recipient label",
      createFieldsHelp: "Blank owner/recipient means unassigned (null). Metadata must be an object; {'{}'} is empty, not null.",
      readIssueAgain: "Read current issue again",
      draftWarning: "No automatic retry or rebase. A missing response may mean this action committed. Read the current issue, compare the original base and draft, then explicitly choose whether to submit again.",
      inspectDraft: "Inspect original base and submitted draft",
      readReconcile: "Read current issue to reconcile",
      useRevision: "Use read revision for retained draft (do not submit)",
      discardDraft: "Accept current read and discard retained action",
      submitStatus: "Submit reconciled status action",
      commentUnknown: "Comment outcome unknown. The draft is retained. Comments are append-only, not deduplicated: inspect comment pages before another send.",
      readComments: "Read current comments to reconcile",
      allowAppend: "I checked the comments; allow another append",
      statusComment: "Status outcome comment (optional)",
      statusMetadata: "Status comment metadata (JSON object)",
      statusHelp: "The next status action submits this optional outcome with its read revision in one transaction. Rejection uses its separate required reason.",
      returnDraft: "Return to the original card to reconcile this draft",
      descriptionHelp: "An empty description clears the body. Unchanged fields are omitted from the patch.",
      editMetadataHelp: "Unchanged metadata is preserved. {'{}'} explicitly replaces the full object; null is not admitted.",
      editOwnerHelp: "An empty owner or recipient clears that assignment with null. Labels do not grant permissions.",
      editComment: "Edit comment (optional)",
      editCommentMetadata: "Edit comment metadata (JSON object)",
      editCommitHelp: "The edit and optional comment commit together or neither commits. A repeated comment-only send is not deduplicated.",
      actor: "Actor",
      labels: "Labels",
      viewMetadata: "View full issue metadata",
      commentMetadata: "Comment metadata (JSON object)",
      commentShortcut: "Ctrl/⌘+Enter appends a comment. A repeated send is not deduplicated.",
      emptyComments: "No comments on this page. This is a live offset view; earlier pages may still contain comments.",
      oversizedComment: "Oversized intact comment exceeds the 4 MiB page target.",
      viewOversizedComment: "View full oversized comment record",
      viewCommentMetadata: "View comment metadata",
      previousComments: "Previous comments page",
      nextComments: "Next comments page",
      history: "History",
      oversizedHistory: "Oversized intact history record exceeds the 4 MiB page target.",
      emptyHistory: "No history records on this page.",
      viewHistory: "View full history record values",
      authorizedValues: "Only returned authorized values are shown. Absent historical values are unknown.",
      previousHistory: "Previous history page",
      nextHistory: "Next history page",
      pagingHelp: "Paging is live, not a snapshot. Concurrent writes can move records. No pages are loaded automatically.",
      authorizedRows: "{range} · {count} authorized rows",
      revisionCard: "Revision {revision} · open the card for its full description and metadata.",
      ownerRecipient: "Owner: {owner} · recipient: {recipient}",
      unassigned: "Unassigned",
      retainedIntent: "Original intent retained ({outcome}). Editing cannot change its payload or key.",
      outcome_pending: "pending",
      outcome_rejected: "rejected",
      outcome_confirmed: "confirmed",
      retainedDraft: "Retained draft for issue #{id} · base revision {revision}",
      retainedSource: "Retained action source: {source}. A fresh read does not change this source or the original base.",
      retainedStatus: "Retained action at explicitly selected revision {revision}: {fields}. Its reason may append again if the earlier action committed.",
      editBase: "Edit fields · original base revision {revision}",
      draftSource: "Draft for issue #{id} · action source {source}.",
      commentCount: "{range} · {count} authorized comments · {completion}",
      historyCount: "{range} · {count} authorized history records · {completion}. Completion is from this offset, not proof that earlier pages were loaded.",
      title: "Title",
      description: "Description",
      type: "Type",
      priority: "Priority",
      sourceContext: "Project context (source for human actions)",
      newIssue: "New issue",
      targetProject: "Target project",
      selectTarget: "select target",
      value_task: "task",
      value_bug: "bug",
      value_feature: "feature",
      value_improvement: "improvement",
      value_medium: "medium",
      value_low: "low",
      value_high: "high",
      value_critical: "critical",
      creating: "Creating...",
      create: "Create issue",
      sourceProject: "Source project",
      chooseSource: "Choose source project",
      ownerRecipientLabel: "Owner / recipient labels",
      saveEdit: "Save edit",
      saving: "Saving...",
      cancel: "Cancel",
      comment: "Comment",
      addComment: "Add comment",
      sending: "Sending...",
      sendComment: "Send comment",
      writeComment: "Write an operator comment...",
      unknown: "unknown",
      unknownAgent: "unknown agent",
      noneRecorded: "none recorded",
      previousRevision: "Previous revision: {revision} · {date}",
      historyActor: "Actor: {actor} · source: {source} · agent label: {agent} · session attribution: {session}",
      changedFields: "Changed fields: {fields}",
      commentActor: "Actor: {actor}",
      historyRevision: "Revision {revision} · {action}",
      historyActionCreated: "created",
      historyActionUpdated: "updated",
      comments: "Comments",
    },
    projectCatalog: {
      unresolvedTitle: 'Unresolved records', unresolvedReason: 'Unresolved reason', review: 'Review retained records',
      kind: 'Record kind', memory: 'Memory', document: 'Documents', issue: 'Issues', history: 'History', more: 'Next page', back: 'Return',
      code: 'Code', metadataOnly: 'Retained group metadata. No reader in this review supports its memberships; this does not mean the original data is absent.',
      readerKindsHelp: 'Reader kinds come from retained membership metadata. Availability, integrity and access are checked when reading.',
      unknownProject: 'The project is unavailable or unresolved.', ambiguousName: 'Equal names; a name does not establish project membership.',
      selectionRequired: 'Choose a catalog project before continuing.', loading: 'Loading the catalog and resolving the project…', search: 'Search',
    },
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
      filterAll: 'Global rules only',
      projectAndGlobal: 'Selected project and global rules',
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

export const issueFlowZh: UiCatalog['issueFlow'] = {
  issueHeading: "问题",
  bulkTitle: "批量确认",
  bulkHelp: "只能确认开放的问题。",
  bulkClear: "清除开放问题选择",
  bulkSelect: "选择开放问题（{count}）",
  bulkRunning: "正在确认…",
  bulkSubmit: "确认所选问题（{count}）",
  bulkDone: "已确认 {count} 个开放问题。",
  bulkNeedOpen: "确认前请选择开放的问题。",
  bulkSelectIssue: "选择问题 {id} 以批量确认",
  bulkOnlyOpen: "批量确认仅适用于开放的问题",
  loadingIssues: "正在加载问题…",
  noIssues: "没有符合当前筛选条件的问题。",
  backIssues: "返回问题列表",
  editAction: "编辑",
  acknowledgeAction: "确认",
  resolveAction: "标记为已解决",
  reopenAction: "重新打开",
  closeAction: "关闭",
  rejectAction: "拒绝",
  deleteAction: "删除",
  rejectHeading: "拒绝问题",
  rejecting: "正在拒绝…",
  rejectSubmit: "填写原因并拒绝",
  deleteConfirm: "永久删除问题 #{id}？",
  deleteHelp: "这将永久删除问题及其评论。",
  deleting: "正在删除…",
  deletePermanently: "永久删除",
  noDescription: "没有描述",
  metaHeading: "详情",
  updatedLabel: "已更新",
  createdAt: "创建时间 {date}",
  commentTotal: "{count} 条评论",
  selectForBulk: "选择以批量确认",
  sourceSession: '创建会话（可选）',
  targetSession: '接收方会话（可选）',
  mutationSession: '更改会话（可选）',
  sessionHelp: '会话引用仅记录来源或接收方，不授予权限。',
  sourceSessionFilter: '创建会话筛选',
  participantSessionFilter: '参与者会话筛选',
  targetSessionFilter: '接收方会话筛选',
  "statusActive": "进行中",
  "statusResolved": "已完成 · 待验证",
  "statusClosed": "已关闭",
  "statusOpen": "开放",
  "statusAll": "全部",
  "issueRevision": "问题 #{id} · 修订版本 {revision}",
  "rejectReason": "拒绝原因",
  "rejectPlaceholder": "必须填写原因，并将其保存为评论。",
  "error_INVALID_ARGUMENT": "问题字段、元数据、数字或分页参数无效。提交前请更正。",
  "error_PAYLOAD_TOO_LARGE": "请求超过允许的大小限制。没有数据被截断。",
  "error_UNAUTHENTICATED": "查看或操作问题前请先进行身份验证。",
  "error_FORBIDDEN": "当前身份无权执行此操作。",
  "error_ISSUE_NOT_FOUND": "问题不存在或当前身份无权查看。",
  "error_ACTOR_CHANGED": "保存的身份与当前身份不匹配。保留原身份、键、来源和数据，明确核对后再发送。",
  "error_IDEMPOTENCY_CONFLICT": "请用原身份、来源、键和数据核对原创建请求。不要创建替代问题。",
  "error_REVISION_CONFLICT": "保留原始版本和草稿。读取当前问题并明确核对后再提交。",
  "error_INVALID_TRANSITION": "读取当前问题并选择允许的状态转换。",
  "error_REVISION_REQUIRED": "保留草稿，在提交实质修改前与读取的修订版本明确核对。",
  "error_ISSUE_GONE": "原问题已删除。保留的请求不得创建替代问题。",
  "error_DETAIL_PAGING_REQUIRED": "使用 detail_mode=paged 读取，并分别按各集合的续页标记继续。",
  "error_PRIVACY_REJECTED": "问题写入被适用的隐私策略拒绝。数据未被泄露或静默删改。",
  "error_TRANSPORT_RECORD_TOO_LARGE": "通过授权的 REST 详情分页读取完整记录。",
  "error_UNAVAILABLE": "结果未知。保留原请求或原始版本及草稿；恢复后明确核对。",
  "error_UNKNOWN_OUTCOME": "结果未知。保留原请求或原始版本及草稿；再次写入前明确核对。",
  "error_INVALID_RESPONSE": "无法确认问题响应。保留原请求或原始版本及草稿并明确核对。",
  "emptyOffset": "偏移量 {offset} 的页面为空",
  "reconcileDeleted": "完成核对：原问题已删除",
  "reconcileCreate": "核对原创建请求",
  "actorChangedResult": "ACTOR_CHANGED：已保留结果。恢复原身份和来源以完成核对。",
  "resultUnavailable": "已保留创建结果，但恢复存储或身份验证不可用。不要创建其他任务。",
  "authChangedPending": "ACTOR_CHANGED：等待恢复期间身份验证已改变。请重新验证原身份和来源；没有重放请求。",
  "actorChangedStored": "ACTOR_CHANGED：保留的请求属于其他身份或规范来源。请恢复该上下文；没有发送重放或替代请求。",
  "intentRestored": "已从当前浏览器标签页恢复原请求。尚未再次提交。",
  "recoveryUnavailable": "恢复不可用。保存的请求未被覆盖或发送。继续前请恢复浏览器存储及原身份和来源。",
  "actorChangedRevise": "ACTOR_CHANGED：修改被拒绝的请求前请恢复原身份。没有发送任何内容。",
  "rejectedNoWrites": "被拒绝的请求未写入数据。请检查保留的表单；下次明确创建将开始新请求。",
  "actorChangedKey": "ACTOR_CHANGED：请恢复原身份。原键和数据仍已保存；没有发送任何内容。",
  "deletedReconciled": "原问题已删除。已明确完成核对；未创建替代问题。",
  "createRequired": "请选择规范来源项目、标题和目标项目。",
  "authChangedBefore": "ACTOR_CHANGED：提交前身份验证已改变。没有发送任何内容。",
  "actorRequired": "UNAUTHENTICATED：需要经过验证的问题操作身份。没有发送任何内容。",
  "sourceChanged": "提交前来源已改变。没有发送任何内容。",
  "createInvalid": "INVALID_ARGUMENT：请检查 JSON 字段、规范项目和浏览器恢复存储。没有发送任何内容。",
  "confirmed": "问题 #{id} 已确认。明确创建的新任务将使用新键。",
  "commentsComplete": "没有后续评论",
  "commentsRemain": "仍有后续评论",
  "historyComplete": "没有后续历史记录",
  "historyRemain": "仍有后续历史记录",
  "unknownReference": "未知引用",
  "unknownActor": "未知身份（历史归属信息不可用）",
  "revisionRequired": "REVISION_REQUIRED：提交实质修改前请读取有效的十进制修订版本。",
  "titleRequired": "INVALID_ARGUMENT：标题不能为空。",
  "editInvalid": "INVALID_ARGUMENT：元数据必须为 JSON 对象，标签必须为 JSON 字符串数组。评论元数据需要非空评论。没有发送任何内容。",
  "statusInvalid": "INVALID_ARGUMENT：状态评论元数据必须为 JSON 对象，且评论不能为空。没有发送任何内容。",
  "revisionSelected": "已明确选择当前修订版本。请检查保留的草稿；没有重新提交。",
  "readAccepted": "已明确接受当前读取结果。保留的操作已丢弃，未重新提交。",
  "anotherAppend": "再次发送将追加一条评论；评论不会自动去重。",
  "commentAdded": "评论已添加。再次发送将追加另一条评论。",
  "rejectRequired": "INVALID_ARGUMENT：拒绝时必须填写原因，该原因将保存为评论。",
  "fieldsUpdated": "问题字段已更新。",
  "commentOnlyAdded": "已追加评论，问题修订版本未改变。",
  "noChanges": "无需实质修改。",
  "statusChanged": "问题状态已更改为 {status}。",
  "intro": "查找、继续和更新问题任务，并明确处理冲突和创建恢复。",
  "humanLabels": "human 来源标签",
  "search": "搜索标题或内容",
  "ownerFilter": "负责人标签筛选",
  "recipientFilter": "接收方标签筛选",
  "issueType": "问题类型",
  "allTypes": "所有类型",
  "labelsDisclaimer": "来源代理、负责人和接收方是标签，不是已验证身份或权限。实时分页不是快照；并发更改后请重新开始。",
  "readIssuesAgain": "重新读取问题",
  "previousIssues": "上一页问题",
  "nextIssues": "下一页问题",
  "restartList": "重新浏览实时列表",
  "createPrivacy": "发送前，此标签页会保存原数据及不含秘密的身份和来源绑定，以便刷新后恢复。请勿输入凭据或保险库资料。不会自动重试或创建替代任务。",
  "editRejected": "写入被拒绝后修改草稿",
  "recoveryHeld": "恢复已暂停。请恢复原身份、规范来源和此标签页的存储。不会自动重放或替换。",
  "checkRecovery": "再次检查恢复上下文",
  "labelsJson": "标签（JSON 数组）",
  "metadataJson": "元数据（JSON 对象）",
  "ownerLabel": "负责人标签",
  "recipientLabel": "接收方标签",
  "createFieldsHelp": "空负责人或接收方表示未分配（null）。元数据必须为对象；{'{}'} 是空对象，不是 null。",
  "readIssueAgain": "重新读取当前问题",
  "draftWarning": "不会自动重试或变基。没有响应可能意味着操作已提交。请读取当前问题，比较原始版本和草稿，然后明确选择是否再次提交。",
  "inspectDraft": "查看原始版本和已提交草稿",
  "readReconcile": "读取当前问题以核对",
  "useRevision": "将读取的修订版本用于保留草稿（不提交）",
  "discardDraft": "接受当前读取结果并丢弃保留的操作",
  "submitStatus": "提交已核对的状态操作",
  "commentUnknown": "评论结果未知。草稿已保留。评论只能追加且不去重；再次发送前请检查评论页面。",
  "readComments": "读取当前评论以核对",
  "allowAppend": "我已检查评论；允许再次追加",
  "statusComment": "状态结果评论（可选）",
  "statusMetadata": "状态评论元数据（JSON 对象）",
  "statusHelp": "下一次状态操作将使用读取的修订版本，在同一事务中提交此可选结果。拒绝时需单独填写原因。",
  "returnDraft": "返回原问题以核对草稿",
  "descriptionHelp": "空描述会清除内容。未更改的字段不会包含在更新请求中。",
  "editMetadataHelp": "未更改的元数据将保留。{'{}'} 会明确替换整个对象；不允许 null。",
  "editOwnerHelp": "空负责人或接收方会使用 null 清除分配。标签不授予权限。",
  "editComment": "编辑评论（可选）",
  "editCommentMetadata": "编辑评论元数据（JSON 对象）",
  "editCommitHelp": "编辑和可选评论一起提交，否则均不提交。仅评论的重复发送不会去重。",
  "actor": "操作身份",
  "labels": "标签",
  "viewMetadata": "查看完整问题元数据",
  "commentMetadata": "评论元数据（JSON 对象）",
  "commentShortcut": "Ctrl/⌘+Enter 追加评论。重复发送不会去重。",
  "emptyComments": "此页没有评论。这是实时偏移视图；前面的页面可能仍有评论。",
  "oversizedComment": "完整评论超过 4 MiB 页面目标。",
  "viewOversizedComment": "查看完整的超大评论记录",
  "viewCommentMetadata": "查看评论元数据",
  "previousComments": "上一页评论",
  "nextComments": "下一页评论",
  "history": "历史记录",
  "oversizedHistory": "完整历史记录超过 4 MiB 页面目标。",
  "emptyHistory": "此页没有历史记录。",
  "viewHistory": "查看完整历史记录值",
  "authorizedValues": "仅显示返回的授权值。缺失的历史值为未知。",
  "previousHistory": "上一页历史记录",
  "nextHistory": "下一页历史记录",
  "pagingHelp": "分页是实时的，不是快照。并发写入可能移动记录。不会自动加载页面。",
  "authorizedRows": "{range} · {count} 条授权记录",
  "revisionCard": "修订版本 {revision} · 打开问题查看完整描述和元数据。",
  "ownerRecipient": "负责人：{owner} · 接收方：{recipient}",
  "unassigned": "未分配",
  "retainedIntent": "已保留原请求（{outcome}）。编辑不能更改其数据或键。",
  "outcome_pending": "待定",
  "outcome_rejected": "已拒绝",
  "outcome_confirmed": "已确认",
  "retainedDraft": "问题 #{id} 的保留草稿 · 基础修订版本 {revision}",
  "retainedSource": "保留操作的来源：{source}。重新读取不会更改此来源或原始版本。",
  "retainedStatus": "明确选择修订版本 {revision} 的保留操作：{fields}。如果之前的操作已提交，其原因可能再次追加。",
  "editBase": "编辑字段 · 原始修订版本 {revision}",
  "draftSource": "问题 #{id} 的草稿 · 操作来源 {source}。",
  "commentCount": "{range} · {count} 条授权评论 · {completion}",
  "historyCount": "{range} · {count} 条授权历史记录 · {completion}。完成状态仅从此偏移量开始，不证明已加载前面的页面。",
  "title": "标题",
  "description": "描述",
  "type": "类型",
  "priority": "优先级",
  "sourceContext": "项目上下文（人工操作的来源）",
  "newIssue": "新建问题",
  "targetProject": "目标项目",
  "selectTarget": "选择目标",
  "value_task": "任务",
  "value_bug": "缺陷",
  "value_feature": "功能",
  "value_improvement": "改进",
  "value_medium": "中",
  "value_low": "低",
  "value_high": "高",
  "value_critical": "严重",
  "creating": "正在创建…",
  "create": "创建问题",
  "sourceProject": "来源项目",
  "chooseSource": "选择来源项目",
  "ownerRecipientLabel": "负责人 / 接收方标签",
  "saveEdit": "保存修改",
  "saving": "正在保存…",
  "cancel": "取消",
  "comment": "评论",
  "addComment": "添加评论",
  "sending": "正在发送…",
  "sendComment": "发送评论",
  "writeComment": "填写操作员评论…",
  "unknown": "未知",
  "unknownAgent": "未知代理",
  "noneRecorded": "无记录",
  "previousRevision": "上一修订版本：{revision} · {date}",
  "historyActor": "身份：{actor} · 来源：{source} · 代理标签：{agent} · 会话归属：{session}",
  "changedFields": "更改字段：{fields}",
  "commentActor": "身份：{actor}",
  "historyRevision": "修订版本 {revision} · {action}",
  "historyActionCreated": "已创建",
  "historyActionUpdated": "已更新",
  "comments": "评论"
}

export const projectCatalogZh: UiCatalog['projectCatalog'] = {
  unresolvedTitle: '未解析的记录',
  unresolvedReason: '未解析的原因',
  review: '查看保留的记录',
  kind: '记录类型',
  memory: '记忆',
  document: '文档',
  issue: '问题',
  code: '代码',
  metadataOnly: '保留的是分组元数据。此视图没有支持其成员记录的读取器；这不表示原始数据不存在。',
  readerKindsHelp: '可读取的类型来自保留的成员关系元数据。读取时会检查可用性、完整性和访问权限。',
  history: '历史记录',
  more: '下一页',
  back: '返回',
  unknownProject: '项目不可用或尚未解析。',
  ambiguousName: '名称相同；不能根据名称确定项目归属。',
  selectionRequired: '继续前请从目录中选择项目。',
  loading: '正在加载目录并解析项目…',
  search: '搜索',
}
