import { useState } from 'react'
import {
  useExportStatus,
  useStartUsageExport,
  type UsageParams,
} from '@/api/console'

/**
 * 202 -> poll -> download. The POST returns a signed task token; polling that
 * token yields DONE plus a signed URL. A second request inside the one-hour
 * lock answers 409 and the page shows the backend's message and hint as-is.
 */
export function useUsageExport(params: UsageParams) {
  const start = useStartUsageExport()
  const [taskId, setTaskId] = useState<string | null>(null)
  const status = useExportStatus(taskId)

  const begin = async () => {
    try {
      const response = await start.mutateAsync(params)
      setTaskId(response.task_id)
    } catch {
      // Failure is surfaced through start.error so the caller renders the
      // backend message and hint instead of inventing a retry copy.
    }
  }

  const reset = () => {
    setTaskId(null)
    start.reset()
  }

  return { begin, reset, start, taskId, status }
}
