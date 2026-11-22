import { useEffect, useRef, useState } from 'react'
import { MoreHorizontal, type LucideIcon } from 'lucide-react'

import { IconButton } from './IconButton'
import styles from './Menu.module.css'

export interface MenuItem {
  readonly id: string
  readonly label: string
  readonly icon?: LucideIcon
  /** Destructive commands read in the danger color. */
  readonly danger?: boolean
  readonly disabled?: boolean
  readonly onSelect: () => void
}

export interface MenuProps {
  /** Accessible name shared by the trigger and the popup. */
  readonly label: string
  readonly items: readonly MenuItem[]
  readonly triggerIcon?: LucideIcon
}

/**
 * Menu collapses low-frequency commands behind one icon trigger.
 * The popup closes on selection, Escape or any click outside; it is
 * the same disclosure pattern the kanban card menu uses, promoted to the
 * design kit so page headers stop tiling text buttons.
 */
export function Menu({ label, items, triggerIcon = MoreHorizontal }: MenuProps): JSX.Element {
  const [open, setOpen] = useState(false)
  const rootRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return
    const onPointerDown = (event: MouseEvent): void => {
      if (rootRef.current !== null && !rootRef.current.contains(event.target as Node)) {
        setOpen(false)
      }
    }
    const onKeyDown = (event: KeyboardEvent): void => {
      if (event.key === 'Escape') setOpen(false)
    }
    document.addEventListener('mousedown', onPointerDown)
    document.addEventListener('keydown', onKeyDown)
    return () => {
      document.removeEventListener('mousedown', onPointerDown)
      document.removeEventListener('keydown', onKeyDown)
    }
  }, [open])

  return (
    <div className={styles.root} ref={rootRef}>
      <IconButton
        icon={triggerIcon}
        label={label}
        active={open}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
      />
      {open && (
        <div className={styles.popup} role="menu" aria-label={label}>
          {items.map((item) => {
            const Icon = item.icon
            return (
              <button
                key={item.id}
                type="button"
                role="menuitem"
                disabled={item.disabled}
                className={[styles.item, item.danger === true ? styles.itemDanger : ''].join(' ')}
                onClick={() => {
                  setOpen(false)
                  item.onSelect()
                }}
              >
                {Icon !== undefined && <Icon aria-hidden="true" className={styles.itemIcon} strokeWidth={2} />}
                <span>{item.label}</span>
              </button>
            )
          })}
        </div>
      )}
    </div>
  )
}
