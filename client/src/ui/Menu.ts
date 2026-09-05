/**
 * A floating menu, used by the launcher and by context menus.
 *
 * One menu is open at a time; opening another, clicking away, pressing Escape
 * or scrolling all dismiss it.
 */

export interface MenuItem {
  label: string
  onSelect?: () => void
  disabled?: boolean
}

export const SEPARATOR: MenuItem = {label: '-'}

let open: HTMLElement | null = null

export const closeMenu = (): void => {
  open?.remove()
  open = null
  window.removeEventListener('pointerdown', onPointerDown)
  window.removeEventListener('keydown', onKeyDown)
  window.removeEventListener('blur', closeMenu)
}

export const showMenu = (items: MenuItem[], at: {x: number; y: number}): void => {
  closeMenu()

  const menu = document.createElement('div')
  menu.className = 'menu'
  menu.setAttribute('role', 'menu')

  for (const item of items) {
    if (item === SEPARATOR) {
      menu.append(document.createElement('hr'))
      continue
    }

    const button = document.createElement('button')
    button.type = 'button'
    button.className = 'menu-item'
    button.textContent = item.label
    button.disabled = item.disabled === true
    button.addEventListener('click', () => {
      closeMenu()
      item.onSelect?.()
    })
    menu.append(button)
  }

  // Measure off-screen so the menu can be flipped before it is ever painted.
  menu.style.visibility = 'hidden'
  document.body.append(menu)
  open = menu

  const {width, height} = menu.getBoundingClientRect()
  menu.style.left = `${Math.min(at.x, Math.max(window.innerWidth - width, 0))}px`
  menu.style.top = `${Math.min(at.y, Math.max(window.innerHeight - height, 0))}px`
  menu.style.visibility = 'visible'

  // Deferred, so the click or contextmenu that opened this does not close it.
  //
  // Registered without `once`, which removes a listener on the first event of
  // its type rather than the first dismissing one: a pointerdown on the menu's
  // own padding, or any key that is not Escape, would otherwise consume the
  // handler and leave the menu with no way to be dismissed. closeMenu takes
  // them all off instead.
  setTimeout(() => {
    window.addEventListener('pointerdown', onPointerDown)
    window.addEventListener('keydown', onKeyDown)
    window.addEventListener('blur', closeMenu)
  }, 0)
}

const onPointerDown = (event: PointerEvent): void => {
  if (open !== null && !open.contains(event.target as Node)) {
    closeMenu()
  }
}

const onKeyDown = (event: KeyboardEvent): void => {
  if (event.key === 'Escape') {
    closeMenu()
  }
}
