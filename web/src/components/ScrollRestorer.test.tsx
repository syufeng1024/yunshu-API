import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import {
  Link,
  Outlet,
  RouterProvider,
  createMemoryRouter,
  useNavigate,
} from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ScrollRestorer } from './ScrollRestorer'

/**
 * jsdom has no layout engine: window.scrollY is always 0 and window.scrollTo is
 * a no-op. Both are overridden with a single mutable value so the assertions can
 * observe the calls <ScrollRestoration /> makes (0 on PUSH, saved offset on POP).
 */
let scrollYValue = 0

function TestLayout() {
  const navigate = useNavigate()
  return (
    <div>
      <ScrollRestorer />
      <Link to="/first">去第一页</Link>
      <Link to="/second">去第二页</Link>
      <button
        type="button"
        onClick={() => {
          // router.navigate(-1) is the memory-router equivalent of history.back();
          // it dispatches a POP, which is the action <ScrollRestoration /> restores for.
          navigate(-1)
        }}
      >
        返回
      </button>
      <Outlet />
    </div>
  )
}

function renderRouter() {
  const router = createMemoryRouter(
    [
      {
        element: <TestLayout />,
        children: [
          { path: '/first', element: <p>第一页内容</p> },
          { path: '/second', element: <p>第二页内容</p> },
        ],
      },
    ],
    { initialEntries: ['/first'] },
  )
  render(<RouterProvider router={router} />)
  return router
}

beforeEach(() => {
  scrollYValue = 0
  Object.defineProperty(window, 'scrollY', {
    configurable: true,
    get: () => scrollYValue,
  })
  vi.spyOn(window, 'scrollTo').mockImplementation(
    ((x: number | ScrollToOptions, y?: number) => {
      scrollYValue = typeof x === 'object' ? (x.top ?? 0) : (y ?? 0)
    }) as typeof window.scrollTo,
  )
})

afterEach(() => {
  vi.restoreAllMocks()
  sessionStorage.clear()
})

describe('ScrollRestorer', () => {
  it('resets a PUSH navigation to the top of the new page', async () => {
    const user = userEvent.setup()
    renderRouter()

    scrollYValue = 400
    await user.click(screen.getByRole('link', { name: '去第二页' }))

    expect(await screen.findByText('第二页内容')).toBeInTheDocument()
    await waitFor(() => {
      expect(window.scrollTo).toHaveBeenCalledWith(0, 0)
    })
    expect(window.scrollY).toBe(0)
  })

  it('restores the saved position after a POP (back) navigation', async () => {
    const user = userEvent.setup()
    renderRouter()

    scrollYValue = 400
    await user.click(screen.getByRole('link', { name: '去第二页' }))
    expect(await screen.findByText('第二页内容')).toBeInTheDocument()
    await waitFor(() => {
      expect(window.scrollY).toBe(0)
    })

    // Scroll down on the second page before going back.
    scrollYValue = 725
    await user.click(screen.getByRole('button', { name: '返回' }))

    expect(await screen.findByText('第一页内容')).toBeInTheDocument()
    await waitFor(() => {
      expect(window.scrollTo).toHaveBeenCalledWith(0, 400)
    })
    expect(window.scrollY).toBe(400)
  })
})
