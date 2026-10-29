import { ScrollRestoration } from 'react-router-dom'

/**
 * D-26: use the data router's built-in <ScrollRestoration /> instead of a
 * `useEffect(() => window.scrollTo(0, 0), [pathname])`. The built-in component
 * scrolls to the top on PUSH/REPLACE and restores the saved position on POP
 * (browser back/forward); an effect on pathname cannot express the difference.
 * It must render under the data router created in `src/router.tsx`.
 */
export function ScrollRestorer() {
  return <ScrollRestoration />
}
