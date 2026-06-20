<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Lock, MonitorCog, RefreshCw, Settings2, Sun, Moon, Monitor as MonitorIcon } from 'lucide-vue-next'
import { useAuth } from '@/composables/useAuth'
import { useColorMode } from '@/composables/useColorMode'
import { useConsoleDensity } from '@/composables/useConsoleDensity'
import { useUiI18n } from '@/composables/useUiI18n'
import { fetchConfig } from '@/utils/api'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import {
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
} from '@/components/ui/tabs'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

const { authDisabled, isAdmin, user } = useAuth()
const { preference, cycleColorMode } = useColorMode()
const { density, setDensity } = useConsoleDensity()
const { locale, t } = useUiI18n()

const activeTab = ref('general')
const config = ref<Record<string, Record<string, unknown>> | null>(null)
const configLoading = ref(false)
const configError = ref<string | null>(null)

const themeOptions = computed(() => [
  { value: 'light', label: t.value.common.theme.light, icon: Sun },
  { value: 'dark', label: t.value.common.theme.dark, icon: Moon },
  { value: 'auto', label: t.value.common.theme.auto, icon: MonitorIcon },
])

const localeLabel = computed(() => t.value.settings.general.currentLocale[locale.value])

async function loadConfig() {
  configLoading.value = true
  configError.value = null

  try {
    config.value = await fetchConfig()
  } catch (err: any) {
    configError.value = err?.message || t.value.settings.server.configEmpty
    config.value = null
  } finally {
    configLoading.value = false
  }
}

function setTheme(value: string) {
  while (preference.value !== value) {
    cycleColorMode()
  }
}

onMounted(async () => {
  await loadConfig()
})
</script>

<template>
  <div class="space-y-4 pt-4">
    <div class="flex items-center gap-3">
      <Settings2 class="size-5 text-primary" />
      <div>
        <h1 class="text-lg font-semibold">{{ t.settings.title }}</h1>
        <p class="text-sm text-muted-foreground">
          {{ t.settings.subtitle }}
        </p>
      </div>
    </div>

    <Tabs v-model="activeTab" class="space-y-4">
      <TabsList class="grid w-full max-w-md grid-cols-3">
        <TabsTrigger value="general">{{ t.settings.tabs.general }}</TabsTrigger>
        <TabsTrigger value="access">{{ t.settings.tabs.access }}</TabsTrigger>
        <TabsTrigger value="server">{{ t.settings.tabs.server }}</TabsTrigger>
      </TabsList>

      <TabsContent value="general" class="space-y-4">
        <Card>
          <CardHeader class="pb-3">
            <CardTitle class="text-sm font-medium">{{ t.settings.general.title }}</CardTitle>
            <CardDescription>{{ t.settings.general.description }}</CardDescription>
          </CardHeader>
          <CardContent class="grid gap-4 lg:grid-cols-3">
            <div class="space-y-2 rounded-lg border p-4">
              <p class="text-sm font-medium">{{ t.settings.general.themeTitle }}</p>
              <p class="text-xs text-muted-foreground">{{ t.settings.general.themeDescription }}</p>
              <Select :model-value="preference" @update:model-value="(value: any) => setTheme(String(value))">
                <SelectTrigger class="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem v-for="option in themeOptions" :key="option.value" :value="option.value">
                    <div class="flex items-center gap-2">
                      <component :is="option.icon" :size="14" />
                      {{ option.label }}
                    </div>
                  </SelectItem>
                </SelectContent>
              </Select>
            </div>

            <div class="space-y-2 rounded-lg border p-4">
              <p class="text-sm font-medium">{{ t.settings.general.densityTitle }}</p>
              <p class="text-xs text-muted-foreground">{{ t.settings.general.densityDescription }}</p>
              <div class="flex gap-2">
                <Button
                  variant="outline"
                  class="flex-1"
                  :class="density === 'comfortable' ? 'border-primary text-primary' : ''"
                  @click="setDensity('comfortable')"
                >
                  {{ t.common.density.comfortable }}
                </Button>
                <Button
                  variant="outline"
                  class="flex-1"
                  :class="density === 'compact' ? 'border-primary text-primary' : ''"
                  @click="setDensity('compact')"
                >
                  {{ t.common.density.compact }}
                </Button>
              </div>
            </div>

            <div class="space-y-2 rounded-lg border p-4">
              <p class="text-sm font-medium">{{ t.settings.general.localeTitle }}</p>
              <p class="text-xs text-muted-foreground">{{ t.settings.general.localeDescription }}</p>
              <div class="flex items-center gap-2">
                <Badge variant="secondary">{{ localeLabel }}</Badge>
                <Badge variant="outline">{{ t.common.readOnly }}</Badge>
              </div>
              <p class="text-xs text-muted-foreground">{{ t.settings.general.localeReadiness }}</p>
            </div>
          </CardContent>
        </Card>
      </TabsContent>

      <TabsContent value="access" class="space-y-4">
        <Card>
          <CardHeader class="pb-3">
            <CardTitle class="text-sm font-medium">{{ t.settings.access.title }}</CardTitle>
            <CardDescription>{{ t.settings.access.description }}</CardDescription>
          </CardHeader>
          <CardContent class="grid gap-4 lg:grid-cols-2">
            <div class="space-y-3 rounded-lg border p-4">
              <div class="flex items-center gap-2">
                <Lock class="size-4 text-muted-foreground" />
                <p class="text-sm font-medium">{{ t.settings.access.authModeTitle }}</p>
              </div>
              <div class="flex items-center gap-2">
                <Badge :variant="authDisabled ? 'destructive' : 'secondary'">
                  {{ authDisabled ? t.settings.access.authDisabled : t.settings.access.authEnabled }}
                </Badge>
                <Badge variant="outline">{{ t.common.liveBacked }}</Badge>
              </div>
            </div>

            <div class="space-y-3 rounded-lg border p-4">
              <div class="flex items-center gap-2">
                <MonitorCog class="size-4 text-muted-foreground" />
                <p class="text-sm font-medium">{{ t.settings.access.currentUserTitle }}</p>
              </div>
              <div class="space-y-1 text-sm">
                <p class="font-medium">{{ user?.email || t.app.identityFallback }}</p>
                <p class="text-muted-foreground">{{ t.settings.access.roleLabel }}: {{ user?.role || t.app.identityRoleFallback }}</p>
                <p class="text-muted-foreground">{{ t.settings.access.adminLabel }}: {{ isAdmin ? t.settings.access.yes : t.settings.access.no }}</p>
              </div>
            </div>
          </CardContent>
        </Card>

        <Card class="border-dashed">
          <CardContent class="pt-6 text-sm text-muted-foreground">
            {{ t.settings.access.followup }}
          </CardContent>
        </Card>
      </TabsContent>

      <TabsContent value="server" class="space-y-4">
        <Card>
          <CardHeader class="pb-3">
            <div class="flex items-center justify-between gap-3">
              <div>
                <CardTitle class="text-sm font-medium">{{ t.settings.server.configTitle }}</CardTitle>
                <CardDescription>{{ t.settings.server.configDescription }}</CardDescription>
              </div>
              <Button variant="outline" size="sm" :disabled="configLoading" @click="loadConfig">
                <RefreshCw :class="['size-4', configLoading && 'animate-spin']" />
                {{ t.settings.server.refresh }}
              </Button>
            </div>
          </CardHeader>
          <CardContent class="space-y-3">
            <div class="flex items-center gap-2">
              <Badge variant="outline">{{ t.common.readOnly }}</Badge>
              <p class="text-xs text-muted-foreground">{{ t.settings.server.readOnlyNote }}</p>
            </div>

            <div v-if="configLoading" class="text-sm text-muted-foreground">
              {{ t.common.loading }}
            </div>

            <div v-else-if="configError" class="rounded-lg border border-destructive/30 bg-destructive/10 p-4 text-sm text-destructive">
              {{ configError }}
            </div>

            <div v-else-if="config" class="grid gap-3 lg:grid-cols-2">
              <div v-for="(section, key) in config" :key="key" class="rounded-lg border p-4">
                <p class="mb-2 text-xs font-medium uppercase tracking-wide text-muted-foreground">
                  {{ key }}
                </p>
                <div class="space-y-2 text-xs font-mono">
                  <div v-for="(value, field) in section" :key="field" class="flex gap-3">
                    <span class="min-w-[140px] text-muted-foreground">{{ field }}</span>
                    <span class="break-all">{{ value }}</span>
                  </div>
                </div>
              </div>
            </div>

            <div v-else class="text-sm text-muted-foreground">
              {{ t.settings.server.configEmpty }}
            </div>
          </CardContent>
        </Card>
      </TabsContent>
    </Tabs>
  </div>
</template>
