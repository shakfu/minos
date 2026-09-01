/**
 * The top bar: launcher, window list, status.
 *
 * The window list is rebuilt from the manager's state on every change rather
 * than patched, because the list is never more than a handful of buttons.
 */

import {APPS, type AppContext} from '../apps'
import type {WindowManager} from '../wm/WindowManager'
import type {Window} from '../wm/Window'
import {SEPARATOR, showMenu} from './Menu'

export class Panel {
  readonly el = document.createElement('header')

  readonly #windows = document.createElement('nav')
  readonly #status = document.createElement('div')
  readonly #connection = document.createElement('span')
  readonly #clock = document.createElement('span')
  readonly #context: AppContext
  readonly #manager: WindowManager

  #timer: ReturnType<typeof setInterval> | null = null

  constructor(context: AppContext, onLogout: () => void) {
    this.#context = context
    this.#manager = context.wm

    this.el.className = 'panel'
    this.#windows.className = 'panel-windows'
    this.#status.className = 'panel-status'
    this.#connection.className = 'panel-connection'
    this.#clock.className = 'panel-clock'

    this.el.append(this.#launcher(), this.#windows, this.#status)
    this.#status.append(this.#connection, this.#user(onLogout), this.#clock)

    this.setConnected(false)
    this.#tick()
    this.#timer = setInterval(() => this.#tick(), 1000)

    for (const event of ['open', 'close', 'focus', 'update'] as const) {
      this.#manager.bus.on(event, () => this.#renderWindows())
    }
    this.#renderWindows()
  }

  setConnected(connected: boolean): void {
    this.#connection.dataset.state = connected ? 'online' : 'offline'
    this.#connection.title = connected ? 'Connected' : 'Disconnected'
  }

  destroy(): void {
    if (this.#timer !== null) {
      clearInterval(this.#timer)
      this.#timer = null
    }
    this.el.remove()
  }

  #launcher(): HTMLButtonElement {
    const button = document.createElement('button')
    button.type = 'button'
    button.className = 'panel-launcher'
    button.textContent = 'minos'
    button.addEventListener('click', () => {
      const box = button.getBoundingClientRect()
      showMenu(
        APPS.map(app => ({label: app.title, onSelect: () => app.open(this.#context)})),
        {x: box.left, y: box.bottom}
      )
    })
    return button
  }

  #user(onLogout: () => void): HTMLButtonElement {
    const button = document.createElement('button')
    button.type = 'button'
    button.className = 'panel-user'
    button.textContent = this.#context.session.username
    button.addEventListener('click', () => {
      const box = button.getBoundingClientRect()
      showMenu([SEPARATOR, {label: 'Log out', onSelect: onLogout}], {x: box.left, y: box.bottom})
    })
    return button
  }

  #renderWindows(): void {
    this.#windows.replaceChildren(
      ...this.#manager.windows.map(window => this.#windowButton(window))
    )
  }

  #windowButton(window: Window): HTMLButtonElement {
    const button = document.createElement('button')
    button.type = 'button'
    button.className = 'panel-window'
    button.textContent = window.title
    button.title = window.title
    button.dataset.focused = String(this.#manager.focused === window && !window.minimized)
    button.dataset.minimized = String(window.minimized)

    button.addEventListener('click', () => {
      if (window.minimized) {
        window.restore()
      } else if (this.#manager.focused === window) {
        window.minimize()
      } else {
        window.focus()
      }
    })

    return button
  }

  #tick(): void {
    this.#clock.textContent = new Intl.DateTimeFormat(undefined, {
      hour: '2-digit',
      minute: '2-digit'
    }).format(new Date())
  }
}
