export function useSettingsModal() {
  const settingsModalOpen = useState<boolean>('settings-modal-open', () => false)
  const settingsModalTab = useState<string>('settings-modal-tab', () => 'general')
  const settingsModalCycle = useState<number>('settings-modal-open-cycle', () => 0)

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
