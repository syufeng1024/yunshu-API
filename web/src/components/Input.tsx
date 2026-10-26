import { forwardRef, useId } from 'react'
import type { InputHTMLAttributes } from 'react'
import { cn } from '@/lib/cn'

export interface InputProps extends InputHTMLAttributes<HTMLInputElement> {
  label?: string
  hint?: string
  error?: string
}

const FIELD =
  'h-10 w-full rounded-lg border bg-surface px-3 text-sm text-ink placeholder:text-ink-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary disabled:cursor-not-allowed disabled:bg-surface-muted'

export const Input = forwardRef<HTMLInputElement, InputProps>(function Input(
  { label, hint, error, id, className, required, ...rest },
  ref,
) {
  const generatedId = useId()
  const inputId = id ?? `input-${generatedId}`
  const hintId = `${inputId}-hint`
  const errorId = `${inputId}-error`
  const describedBy = error ? errorId : hint ? hintId : undefined

  return (
    <div className="w-full space-y-1.5">
      {label ? (
        <label htmlFor={inputId} className="block text-sm font-medium text-ink">
          {label}
          {required ? (
            <span className="ml-0.5 text-danger" aria-hidden="true">
              *
            </span>
          ) : null}
        </label>
      ) : null}
      <input
        ref={ref}
        id={inputId}
        required={required}
        className={cn(FIELD, error ? 'border-danger' : 'border-border-strong', className)}
        aria-invalid={error ? true : undefined}
        aria-describedby={describedBy}
        {...rest}
      />
      {error ? (
        <p id={errorId} className="text-xs text-danger">
          {error}
        </p>
      ) : hint ? (
        <p id={hintId} className="text-xs text-ink-muted">
          {hint}
        </p>
      ) : null}
    </div>
  )
})
