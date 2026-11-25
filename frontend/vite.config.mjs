import { defineConfig } from 'vite'

const backend = process.env.BEACON_API_ORIGIN ?? 'http://127.0.0.1:17001'

export default defineConfig({
  base: '/workspace/',
  server: {
    host: 'localhost',
    proxy: { '/console': { target: backend }, '/static': { target: backend } },
  },
  preview: {
    host: 'localhost',
    proxy: { '/console': { target: backend }, '/static': { target: backend } },
  },
})
