import {defineConfig} from 'vite'
import {svelte} from '@sveltejs/vite-plugin-svelte'

// https://vitejs.dev/config/
export default defineConfig({
  plugins: [svelte()],
  // No index.html fallback: /img must 404 here so `wails dev` hands it to the Go image handler.
  appType: 'mpa',
})
