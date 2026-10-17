import { computed, ref, watch } from 'vue'
import { useProjectsQuery } from '~/composables/useOperatorQueries'
import { resolveProject } from '~/composables/useOperatorApi'
import { useOperatorI18n } from '~/composables/useOperatorI18n'
import { projectLabel, UnresolvedProjectError } from '../../../../ui/src/utils/projectCatalog'

export function useProjectCatalog(defaultSelection: '' | 'all' = '') {
  const route = useRoute()
  const router = useRouter()
  const { t } = useOperatorI18n()
  const projectsQuery = useProjectsQuery()
  const projects = computed(() => projectsQuery.data.value?.items ?? [])
  const unresolved = computed(() => projectsQuery.data.value?.unresolved ?? [])
  const selectedProject = ref('')
  const selectionPending = ref(true)
  const selectionError = ref<string | null>(null)
  const ready = computed(() => !selectionPending.value && !selectionError.value && selectedProject.value !== '')
  let generation = 0

  watch([() => route.query.project, projectsQuery.data, projectsQuery.error], async ([raw, catalog, error]) => {
    const current = ++generation
    selectionPending.value = true
    selectionError.value = null
    selectedProject.value = ''
    if (error) {
      selectionError.value = error.message
      selectionPending.value = false
      return
    }
    if (!catalog) return
    try {
      if (raw !== undefined && typeof raw !== 'string') throw new Error(t('projectCatalog.selectionRequired'))
      const selection = raw === undefined ? defaultSelection : raw
      if (selection === 'all' || selection === '') {
        selectedProject.value = selection
      } else {
        const id = catalog.items.some(item => item.project_id === selection) ? selection : await resolveProject(selection)
        if (current !== generation) return
        if (!catalog.items.some(item => item.project_id === id)) throw new Error(t('projectCatalog.unknownProject'))
        selectedProject.value = id
        if (raw !== id) await router.replace({ query: { ...route.query, project: id } })
      }
    } catch (error) {
      if (current !== generation) return
      selectedProject.value = ''
      if (error instanceof UnresolvedProjectError && catalog.unresolved.some(record => record.reference === error.reference)) {
        await router.replace({ query: { ...route.query, unresolved: error.reference } })
      } else {
        selectionError.value = error instanceof Error ? error.message : t('projectCatalog.unknownProject')
      }
    } finally {
      if (current === generation) selectionPending.value = false
    }
  }, { immediate: true, flush: 'sync' })

  watch(selectedProject, id => {
    if (selectionPending.value || (!id && selectionError.value) || (id !== 'all' && id !== '' && !projects.value.some(item => item.project_id === id))) return
    selectionError.value = null
    if (route.query.project !== id) void router.replace({ query: { ...route.query, project: id } })
  }, { flush: 'sync' })

  const unresolvedReference = computed(() => typeof route.query.unresolved === 'string' ? route.query.unresolved : '')
  function projectName(id: string): string {
    return projectLabel(projects.value, id, t('projectCatalog.unknownProject'), t('projectCatalog.ambiguousName'))
  }
  return { projectsQuery, projects, unresolved, selectedProject, selectionPending, selectionError, ready, unresolvedReference, projectName }
}
