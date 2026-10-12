import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useUiI18n } from './useUiI18n'
import { fetchProjects, resolveProject } from '@/utils/api'
import { projectLabel, UnresolvedProjectError, type ProjectDescriptor, type UnresolvedProject } from '@/utils/projectCatalog'

export function useProjectCatalog(defaultSelection: '' | 'all' = '') {
  const route = useRoute()
  const router = useRouter()
  const { t } = useUiI18n()
  const projects = ref<ProjectDescriptor[]>([])
  const unresolved = ref<UnresolvedProject[]>([])
  const selectedProject = ref('')
  const selectionPending = ref(true)
  const selectionError = ref<string | null>(null)
  let generation = 0

  async function refresh() {
    const current = ++generation
    selectionPending.value = true
    selectionError.value = null
    selectedProject.value = ''
    try {
      const catalog = await fetchProjects()
      if (current !== generation) return
      projects.value = catalog.items
      unresolved.value = catalog.unresolved
      const raw = route.query.project
      if (raw !== undefined && typeof raw !== 'string') throw new Error(t.value.projectCatalog.selectionRequired)
      const selection = raw === undefined ? defaultSelection : raw
      if (selection === 'all' || selection === '') {
        selectedProject.value = selection
      } else {
        const id = projects.value.some(item => item.project_id === selection) ? selection : await resolveProject(selection)
        if (current !== generation) return
        if (!projects.value.some(item => item.project_id === id)) throw new Error(t.value.projectCatalog.unknownProject)
        selectedProject.value = id
        if (raw !== id) await router.replace({ query: { ...route.query, project: id } })
      }
    } catch (error) {
      if (current !== generation) return
      selectedProject.value = ''
      selectionError.value = error instanceof Error ? error.message : t.value.projectCatalog.unknownProject
      if (error instanceof UnresolvedProjectError) {
        await router.replace({ query: { ...route.query, unresolved: error.reference } })
      }
      throw error
    } finally {
      if (current === generation) selectionPending.value = false
    }
  }

  watch(() => route.query.project, () => { void refresh().catch(() => { }) }, { flush: 'sync' })
  watch(selectedProject, id => {
    if (selectionPending.value || (!id && selectionError.value)) return
    if (id !== 'all' && id !== '' && !projects.value.some(item => item.project_id === id)) return
    selectionError.value = null
    if (route.query.project !== id) void router.replace({ query: { ...route.query, project: id } })
  })

  const unresolvedReference = computed(() => typeof route.query.unresolved === 'string' ? route.query.unresolved : '')
  function projectName(id: string): string {
    return projectLabel(projects.value, id, t.value.projectCatalog.unknownProject, t.value.projectCatalog.ambiguousName)
  }
  return { projects, unresolved, selectedProject, selectionPending, selectionError, unresolvedReference, projectName, refresh }
}
