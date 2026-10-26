import { forwardRef, useId } from 'react'
import type { SelectHTMLAttributes } from 'react'
import { cn } from '@/lib/cn'

export interface SelectOption {
  value: string
  label: string
  disabled?: boolean
}

export interface SelectProps extends SelectHTMLAttributes<HTMLSelectElement> {
  label?: string
  hint?: string
  error?: string
  options: readonly SelectOption[]
  placeholder?: string
}

const FIELD =
  'h-10 w-full rounded-lg border bg-surface px-3 text-sm text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary disabled:cursor-not-allowed disabled:bg-surface-muted'

export const Select = forwardRef<HTMLSelectElement, SelectProps>(function Select(
  { label, hint, error, options, placeholder, id, className, required, ...rest },
  ref,
) {
  const generatedId = useId()
  const selectId = id ?? `select-${generatedId}`
  const hintId = `${selectId}-hint`
  const errorId = `${selectId}-error`
  const describedBy = error ? errorId : hint ? hintId : undefined

  return (
    <div className="w-full space-y-1.5">
      {label ? (
        <label htmlFor={selectId} className="block text-sm font-medium text-ink">
          {label}
          {required ? (
            <span className="ml-0.5 text-danger" aria-hidden="true">
              *
            </span>
          ) : null}
        </label>
      ) : null}
      <select
        ref={ref}
        id={selectId}
        required={required}
        className={cn(FIELD, error ? 'border-danger' : 'border-border-strong', className)}
        aria-invalid={error ? true : undefined}
        aria-describedby={describedBy}
        {...rest}
      >
        {placeholder ? (
          <option value="" disabled>
            {placeholder}
          </option>
        ) : null}
        {options.map((option) => (
          <option key={option.value} value={option.value} disabled={option.disabled}>
            {option.label}
          </option>
        ))}
      </select>
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
