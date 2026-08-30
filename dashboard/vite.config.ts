import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// https://vite.dev/config/
export default defineConfig({
    plugins: [react(), tailwindcss()],
    // Relative base, so the built dashboard works under any base path (e.g. "/ddup/")
    base: './',
    resolve: {
        alias: {
            '@': `${import.meta.dirname}/src`,
        },
    },
})
