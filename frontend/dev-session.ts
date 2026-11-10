import type { Plugin } from 'vite'

// Vite serves its own files, so it checks the daemon's session before serving
// dashboard code. Authentication routes/API requests retain their own handlers.
export function dashboardSession(backendURL: string): Plugin {
  return {
    name: 'dashboard-session',
    configureServer(server) {
      server.middlewares.use(async (request, response, next) => {
        const url = new URL(request.url ?? '/', 'http://localhost')
        const pathname = url.pathname
        if (pathname.startsWith('/ui/')) {
          response.setHeader('Content-Security-Policy', "frame-ancestors 'none'")
        }
        if (
          pathname.startsWith('/auth/') ||
          pathname.startsWith('/oauth/callback/') ||
          pathname.startsWith('/api/') ||
          pathname.startsWith('/server-icons/') ||
          pathname === '/ui/favicon.svg'
        ) {
          next()
          return
        }

        try {
          const result = await fetch(new URL('/auth/session', backendURL), {
            headers: { Cookie: request.headers.cookie ?? '' },
            signal: AbortSignal.timeout(5000),
          })
          if (!result.ok) throw new Error('Session check failed')
          const session = await result.json() as { authenticated: boolean }
          if (session.authenticated === true) {
            response.setHeader('Cache-Control', 'private, no-cache')
            next()
            return
          }
          response.setHeader('Cache-Control', 'no-store')
          const document = request.headers['sec-fetch-dest'] === 'document' ||
            request.headers.accept?.includes('text/html')
          if (document) {
            response.writeHead(302, {
              Location: '/auth/login?return_to=' + encodeURIComponent(request.url ?? '/ui/'),
            })
            response.end()
            return
          }
          response.writeHead(401, { 'Content-Type': 'application/json' })
          response.end(JSON.stringify({ error: 'Sign in to continue' }))
        } catch {
          response.writeHead(503, { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' })
          response.end(JSON.stringify({ error: 'Dashboard authentication unavailable' }))
        }
      })
    },
  }
}
