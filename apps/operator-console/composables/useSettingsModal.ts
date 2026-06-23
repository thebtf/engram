export function useSettingsModal() {
  const settingsModalOpen = useState<boolean>('settings-modal-open', () => false)
  const settingsModalTab = useState<string>('settings-modal-tab', () => 'general')

  function openSettingsModal(tab = 'general') {
    settingsModalTab.value = tab
    settingsModalOpen.value = true
  }

  function closeSettingsModal() {
    settingsModalOpen.value = false
  }

  return {
    settingsModalOpen,
    settingsModalTab,
    openSettingsModal,
    closeSettingsModal,
  }
}
