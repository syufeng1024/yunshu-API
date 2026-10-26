import { Suspense } from 'react'
import { QueryClientProvider } from '@tanstack/react-query'
import { RouterProvider } from 'react-router-dom'
import { ToastViewport } from '@/components/Toast'
import { queryClient } from '@/api/queryClient'
import { router } from '@/router'
import { RouteLoading } from '@/router/guards'

export default function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <Suspense fallback={<RouteLoading fullScreen />}>
        <RouterProvider router={router} />
      </Suspense>
      <ToastViewport />
    </QueryClientProvider>
  )
}
