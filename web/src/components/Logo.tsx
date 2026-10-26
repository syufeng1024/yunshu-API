import { Link } from 'react-router-dom'
import { BRAND_NAME, LOGO_ARIA_LABEL, ROUTES } from '@/lib/constants'
import { cn } from '@/lib/cn'

export interface LogoProps {
  /** light = dark wordmark on a light surface; dark = white wordmark. */
  variant?: 'light' | 'dark'
  /** sm = 26×26 mark used by the collapsed console/admin sidebar. */
  size?: 'md' | 'sm'
  /** Hide the wordmark below md (768px) while keeping the mark clickable. */
  collapsible?: boolean
  /** Each end returns to its own home: /, /console or /admin. */
  to?: string
  className?: string
}

export function Logo({
  variant = 'light',
  size = 'md',
  collapsible = false,
  to = ROUTES.home,
  className,
}: LogoProps) {
  const pixels = size === 'sm' ? 26 : 28
  return (
    <Link
      to={to}
      aria-label={LOGO_ARIA_LABEL}
      className={cn(
        'logo inline-flex items-center rounded-md',
        variant === 'dark' ? 'gap-brand-gap-dark' : 'gap-brand-gap',
        className,
      )}
    >
      <img
        src="/assets/logo/svg/yunshu-mark.svg"
        alt=""
        aria-hidden="true"
        width={pixels}
        height={pixels}
        draggable={false}
        className={cn(
          'block shrink-0',
          size === 'sm' ? 'h-mark-sm w-mark-sm rounded-mark-sm' : 'h-mark w-mark rounded-mark',
        )}
      />
      <span
        className={cn(
          'whitespace-nowrap',
          size === 'sm' ? 'text-wordmark-sm' : 'text-wordmark',
          variant === 'dark' ? 'text-sidebar-text' : 'text-ink',
          collapsible && 'hidden md:inline',
        )}
      >
        {BRAND_NAME}
      </span>
    </Link>
  )
}
