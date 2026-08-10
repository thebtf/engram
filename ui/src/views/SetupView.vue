<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { Loader2, AlertCircle } from 'lucide-vue-next'
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Button } from '@/components/ui/button'

const router = useRouter()
const email = ref('')
const operatorToken = ref('')
const password = ref('')
const confirmPassword = ref('')
const error = ref('')
const submitting = ref(false)

async function handleSetup() {
  error.value = ''
  if (!email.value.trim() || !operatorToken.value || !password.value) {
    error.value = 'Email, operator token, and password are required.'
    return
  }
  if (password.value !== confirmPassword.value) {
    error.value = 'Passwords do not match.'
    return
  }
  if (password.value.length < 8) {
    error.value = 'Password must be at least 8 characters.'
    return
  }

  submitting.value = true
  try {
    const resp = await fetch('/api/auth/setup', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'X-Auth-Token': operatorToken.value },
      body: JSON.stringify({ email: email.value.trim(), password: password.value }),
    })
    if (!resp.ok) {
      const data = await resp.json().catch(() => ({}))
      error.value = data.error || 'Setup failed.'
      return
    }
    router.push({ name: 'login' })
  } catch {
    error.value = 'Connection error. Is the server running?'
  } finally {
    submitting.value = false
    operatorToken.value = ''
  }
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
          <CardTitle class="text-2xl">Set up engram</CardTitle>
          <CardDescription>Create the first admin account</CardDescription>
        </CardHeader>

        <CardContent>
          <form @submit.prevent="handleSetup" class="space-y-4">
            <div class="space-y-2">
              <Label for="setup-email">Email</Label>
              <Input
                id="setup-email"
                v-model="email"
                type="email"
                autocomplete="email"
                placeholder="admin@example.com"
                :disabled="submitting"
              />
            </div>

            <div class="space-y-2">
              <Label for="setup-operator-token">Operator Token</Label>
              <Input
                id="setup-operator-token"
                v-model="operatorToken"
                type="password"
                autocomplete="off"
                :disabled="submitting"
              />
            </div>

            <div class="space-y-2">
              <Label for="setup-password">Password</Label>
              <Input
                id="setup-password"
                v-model="password"
                type="password"
                autocomplete="new-password"
                placeholder="Min 8 characters"
                :disabled="submitting"
              />
            </div>

            <div class="space-y-2">
              <Label for="setup-confirm-password">Confirm Password</Label>
              <Input
                id="setup-confirm-password"
                v-model="confirmPassword"
                type="password"
                autocomplete="new-password"
                :disabled="submitting"
              />
            </div>

            <p v-if="error" class="flex items-center gap-2 text-sm text-destructive">
              <AlertCircle class="w-4 h-4 shrink-0" />
              {{ error }}
            </p>

            <Button type="submit" class="w-full" :disabled="submitting">
              <Loader2 v-if="submitting" class="w-4 h-4 mr-2 animate-spin" />
              {{ submitting ? 'Creating account...' : 'Create Admin Account' }}
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  </div>
</template>
