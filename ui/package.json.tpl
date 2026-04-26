{
  "name": "engram-dashboard",
  "version": "{{ .Version }}",
  "private": true,
  "type": "module",
  "scripts": {
    "dev": "vite",
    "build": "vue-tsc -b && vite build",
    "preview": "vite preview",
    "type-check": "vue-tsc --noEmit"
  },
  "dependencies": {
    "@fontsource-variable/inter": "^5.2.8",
    "@fontsource-variable/jetbrains-mono": "^5.2.8",
    "@shikijs/transformers": "^4.0.2",
    "@vueuse/core": "^14.2.1",
    "class-variance-authority": "^0.7.1",
    "clsx": "^2.1.1",
    "dompurify": "^3.3.3",
    "lucide-vue-next": "^1.0.0",
    "marked": "^18.0.0",
    "reka-ui": "^2.9.6",
    "shiki": "^4.0.2",
    "tailwind-merge": "^3.5.0",
    "tailwindcss-animate": "^1.0.7",
    "vue": "^3.5.13",
    "vue-router": "^4.5.0",
    "vue-sonner": "^2.0.9"
  },
  "devDependencies": {
    "@types/dompurify": "^3.0.5",
    "@types/node": "^22.10.2",
    "@vitejs/plugin-vue": "^5.2.1",
    "autoprefixer": "^10.4.20",
    "postcss": "^8.4.49",
    "tailwindcss": "^3.4.17",
    "typescript": "~5.7.2",
    "vite": "^6.0.5",
    "vue-tsc": "^2.2.12"
  }
}
