<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { Loader2, Mail, Key, AlertCircle } from 'lucide-vue-next'
import { useAuth } from '@/composables/useAuth'
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from '@/components/ui/card'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Button } from '@/components/ui/button'

const router = useRouter()
const { login, loginWithCredentials } = useAuth()

// Token login state
const token = ref('')

// Credential login state
const email = ref('')
const password = ref('')

const error = ref('')
const submitting = ref(false)

async function handleTokenLogin() {
  error.value = ''
  if (!token.value.trim()) {
    error.value = 'Please enter your admin token.'
    return
  }

  submitting.value = true
  try {
    const ok = await login(token.value.trim())
    if (ok) {
      router.push({ name: 'home' })
    } else {
      error.value = 'Invalid token. Please try again.'
      token.value = ''
    }
  } catch {
    error.value = 'Connection error. Is the server running?'
  } finally {
    submitting.value = false
  }
}

async function handleEmailLogin() {
  error.value = ''
  if (!email.value.trim() || !password.value) {
    error.value = 'Email and password are required.'
    return
  }

  submitting.value = true
  try {
    const ok = await loginWithCredentials(email.value.trim(), password.value)
    if (ok) {
      router.push({ name: 'home' })
    } else {
      error.value = 'Invalid email or password.'
      password.value = ''
    }
  } catch {
    error.value = 'Connection error. Is the server running?'
  } finally {
    submitting.value = false
  }
}

function handleTabChange() {
  error.value = ''
}
</script>

<template>
  <div class="min-h-screen flex items-center justify-center bg-background px-4">
    <div class="w-full max-w-md">
      <Card>
        <CardHeader class="text-center pb-4">
          <div class="w-14 h-14 mx-auto mb-4 rounded-2xl bg-primary flex items-center justify-center">
            <span class="text-primary-foreground font-bold text-xl">E</span>
          </div>
          <CardTitle class="text-2xl">Sign in to engram</CardTitle>
          <CardDescription>Choose your sign-in method below</CardDescription>
        </CardHeader>

        <CardContent>
          <Tabs default-value="credentials" @update:model-value="handleTabChange">
            <TabsList class="w-full mb-6">
              <TabsTrigger value="credentials" class="flex-1 justify-center">
                <Mail class="w-4 h-4 mr-2" />
                Email
              </TabsTrigger>
              <TabsTrigger value="token" class="flex-1 justify-center">
                <Key class="w-4 h-4 mr-2" />
                Token
              </TabsTrigger>
            </TabsList>

            <!-- Email / Password tab -->
            <TabsContent value="credentials">
              <form @submit.prevent="handleEmailLogin" class="space-y-4">
                <div class="space-y-2">
                  <Label for="email">Email</Label>
                  <Input
                    id="email"
                    v-model="email"
                    type="email"
                    autocomplete="email"
                    placeholder="you@example.com"
                    :disabled="submitting"
                  />
                </div>
                <div class="space-y-2">
                  <Label for="password">Password</Label>
                  <Input
                    id="password"
                    v-model="password"
                    type="password"
                    autocomplete="current-password"
                    placeholder="Enter your password"
                    :disabled="submitting"
                  />
                </div>

                <p v-if="error" class="flex items-center gap-2 text-sm text-destructive">
                  <AlertCircle class="w-4 h-4 shrink-0" />
                  {{ error }}
                </p>

                <Button type="submit" class="w-full" :disabled="submitting">
                  <Loader2 v-if="submitting" class="w-4 h-4 mr-2 animate-spin" />
                  {{ submitting ? 'Logging in...' : 'Login' }}
                </Button>

                <p class="text-center text-sm text-muted-foreground">
                  Have an invitation?
                  <router-link
                    to="/register"
                    class="text-primary hover:text-primary/80 font-medium transition-colors"
                  >
                    Register
                  </router-link>
                </p>
              </form>
            </TabsContent>

            <!-- Token tab -->
            <TabsContent value="token">
              <form @submit.prevent="handleTokenLogin" class="space-y-4">
                <div class="space-y-2">
                  <Label for="token-input">Admin Token</Label>
                  <Input
                    id="token-input"
                    v-model="token"
                    type="password"
                    autocomplete="current-password"
                    placeholder="Enter your admin token"
                    :disabled="submitting"
                  />
                </div>

                <p v-if="error" class="flex items-center gap-2 text-sm text-destructive">
                  <AlertCircle class="w-4 h-4 shrink-0" />
                  {{ error }}
                </p>

                <Button type="submit" class="w-full" :disabled="submitting">
                  <Loader2 v-if="submitting" class="w-4 h-4 mr-2 animate-spin" />
                  {{ submitting ? 'Logging in...' : 'Login' }}
                </Button>
              </form>
            </TabsContent>
          </Tabs>
        </CardContent>
      </Card>
    </div>
  </div>
</template>
