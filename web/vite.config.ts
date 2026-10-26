import { readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath, URL } from 'node:url'
import react from '@vitejs/plugin-react'
import { defineConfig, loadEnv } from 'vite'
import type { Plugin } from 'vite'

/**
 * Hard constraint 6: price, plan and quota values come from the API. A literal
 * like "100 积分", "9.9 元" or "60 RPM" in a shipped chunk is a build failure.
 * Unit suffixes without a number (e.g. "积分/千 token") are allowed.
 */
function assertNoPricingLiterals(): Plugin {
  return {
    name: 'yunshu-assert-no-pricing-literals',
    apply: 'build',
    closeBundle() {
      const assetsDir = fileURLToPath(new URL('./dist/assets', import.meta.url))
      let files: string[] = []
      try {
        files = readdirSync(assetsDir).filter((file) => file.endsWith('.js'))
      } catch {
        return
      }
      const hits: string[] = []
      for (const file of files) {
        const code = readFileSync(join(assetsDir, file), 'utf8')
        const match = code.match(/[0-9]+ ?(?:微积分|积分|元|RPM|TPM|并发|日额度)/i)
        if (match) hits.push(`${file}: ${match[0]}`)
      }
      if (hits.length > 0) {
        throw new Error(
          `Build contains hard-coded price/quota literals. Move them to the API:\n${hits.join('\n')}`,
        )
      }
    },
  }
}

/**
 * Hard constraint 10/23: fixture data is a dev-only preview. The dynamic
 * import is kept out of production by the constant branch in client.ts; this
 * assertion is the backstop that fails the build if it ever slips through.
 */
function assertNoFixtures(): Plugin {
  return {
    name: 'yunshu-assert-no-fixtures',
    apply: 'build',
    closeBundle() {
      const assetsDir = fileURLToPath(new URL('./dist/assets', import.meta.url))
      let files: string[] = []
      try {
        files = readdirSync(assetsDir, { withFileTypes: true })
          .filter((entry) => entry.isFile())
          .map((entry) => entry.name)
      } catch {
        return
      }
      const marker = '__YUNSHU_T18_FIXTURE__'
      const hits = files.filter((file) => readFileSync(join(assetsDir, file), 'utf8').includes(marker))
      if (hits.length > 0) {
        throw new Error(
          `Build contains dev fixture data. Keep it behind import.meta.env.DEV:\n${hits.join('\n')}`,
        )
      }
    },
  }
}

// The control plane owns :8081. /api, /healthz and /readyz are proxied in dev
// so the browser always talks to a same-origin URL. Deployed builds either sit
// behind the same reverse proxy or set VITE_API_BASE_URL to the control origin.
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '')
  const target = env.VITE_DEV_API_TARGET || 'http://localhost:8081'

  const proxy = {
    '/api': { target, changeOrigin: true },
    '/healthz': { target, changeOrigin: true },
    '/readyz': { target, changeOrigin: true },
  }

  return {
    plugins: [react(), assertNoPricingLiterals(), assertNoFixtures()],
    resolve: {
      alias: {
        '@': fileURLToPath(new URL('./src', import.meta.url)),
      },
    },
    server: {
      port: 5173,
      proxy,
    },
    preview: {
      port: 4173,
      proxy,
    },
    build: {
      sourcemap: false,
      rollupOptions: {
        output: {
          manualChunks: {
            react: ['react', 'react-dom', 'react-router-dom'],
            data: ['@tanstack/react-query', 'zustand'],
            charts: ['recharts'],
          },
        },
      },
    },
  }
})
