import { ref as sharedRef, type Ref as SharedRef } from 'vue'

let sharedSettingsModalOpen: SharedRef<boolean> | undefined
let sharedSettingsModalTab: SharedRef<string> | undefined
let sharedSettingsModalOpenCycle: SharedRef<number> | undefined

export function useSettingsModal() {
  const settingsModalOpen = (sharedSettingsModalOpen ??= sharedRef<boolean>((() => false)()))
  const settingsModalTab = (sharedSettingsModalTab ??= sharedRef<string>((() => 'general')()))
  const settingsModalCycle = (sharedSettingsModalOpenCycle ??= sharedRef<number>((() => 0)()))

  function openSettingsModal(tab = 'general') {
    if (!settingsModalOpen.value) settingsModalCycle.value += 1
    settingsModalTab.value = tab
    settingsModalOpen.value = true
  }

  function closeSettingsModal() {
    settingsModalOpen.value = false
  }

  return {
    settingsModalOpen,
    settingsModalCycle,
    settingsModalTab,
    openSettingsModal,
    closeSettingsModal,
  }
}
