import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue()],
  server: {
    proxy: {
      '/api': 'http://127.0.0.1:8082',
      '/ws-ticket': 'http://127.0.0.1:8081',
      '/ws': { target: 'ws://127.0.0.1:8081', ws: true },
    },
  },
})
