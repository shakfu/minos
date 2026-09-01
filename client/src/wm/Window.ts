/**
 * A desktop window: chrome, drag, resize, and the states the panel reflects.
 *
 * Geometry is applied as a transform plus explicit width and height, so moving
 * a window never touches layout.
 */

import type {Rect, WindowManager} from './WindowManager'

export interface WindowOptions {
  title: string
  width?: number
  height?: number
  resizable?: boolean
  minWidth?: number
  minHeight?: number
}

/** Horizontal pixels of a window that must stay inside the workspace. */
const EDGE_KEEP = 80

/** Vertical pixels kept visible, so a title bar is always grabbable. */
const HEADER_KEEP = 28

const clamp = (value: number, min: number, max: number): number =>
  Math.min(Math.max(value, min), Math.max(min, max))

const element = <K extends keyof HTMLElementTagNameMap>(
  tag: K,
  className: string
): HTMLElementTagNameMap[K] => {
  const node = document.createElement(tag)
  node.className = className
  return node
}

let sequence = 0

export class Window {
  readonly id = `win-${++sequence}`
  readonly el = element('section', 'win')
  readonly body = element('div', 'win-body')

  readonly #header = element('header', 'win-header')
  readonly #titleEl = element('span', 'win-title')
  readonly #manager: WindowManager
  readonly #minWidth: number
  readonly #minHeight: number

  #title: string
  #rect: Rect
  #restore: Rect | null = null
  #minimized = false
  #closed = false

  constructor(manager: WindowManager, options: WindowOptions, position: {x: number; y: number}) {
    this.#manager = manager
    this.#title = options.title
    this.#minWidth = options.minWidth ?? 240
    this.#minHeight = options.minHeight ?? 160
    this.#rect = {
      x: position.x,
      y: position.y,
      width: Math.max(options.width ?? 640, this.#minWidth),
      height: Math.max(options.height ?? 420, this.#minHeight)
    }

    this.el.dataset.focused = 'false'
    this.#buildChrome(options.resizable !== false)
    this.#applyRect()
    this.#renderTitle()
  }

  get title(): string {
    return this.#title
  }

  get rect(): Rect {
    return {...this.#rect}
  }

  get minimized(): boolean {
    return this.#minimized
  }

  get maximized(): boolean {
    return this.#restore !== null
  }

  get closed(): boolean {
    return this.#closed
  }

  setTitle(title: string): void {
    this.#title = title
    this.#renderTitle()
    this.#manager.bus.emit('update', this)
  }

  focus(): void {
    this.#manager.focus(this)
  }

  close(): void {
    if (this.#closed) {
      return
    }
    this.#closed = true
    this.#manager.close(this)
  }

  minimize(): void {
    this.#minimized = true
    this.el.hidden = true
    this.#manager.bus.emit('update', this)
    this.#manager.focusTopmost()
  }

  restore(): void {
    this.#minimized = false
    this.el.hidden = false
    this.#manager.bus.emit('update', this)
    this.focus()
  }

  toggleMaximize(): void {
    if (this.#restore === null) {
      this.#restore = {...this.#rect}
      this.#rect = {...this.#manager.workspace}
    } else {
      this.#rect = this.#restore
      this.#restore = null
    }
    this.el.dataset.maximized = String(this.maximized)
    this.#applyRect()
  }

  moveTo(x: number, y: number): void {
    const space = this.#manager.workspace
    this.#rect.x = clamp(x, space.x - this.#rect.width + EDGE_KEEP, space.x + space.width - EDGE_KEEP)
    this.#rect.y = clamp(y, space.y, space.y + space.height - HEADER_KEEP)
    this.#applyRect()
  }

  resizeTo(width: number, height: number): void {
    this.#rect.width = Math.max(width, this.#minWidth)
    this.#rect.height = Math.max(height, this.#minHeight)
    this.#applyRect()
  }

  setFocused(focused: boolean): void {
    this.el.dataset.focused = String(focused)
  }

  setZIndex(z: number): void {
    this.el.style.zIndex = String(z)
  }

  #buildChrome(resizable: boolean): void {
    this.#header.append(this.#titleEl)

    for (const action of ['minimize', 'maximize', 'close'] as const) {
      const button = element('button', 'win-btn')
      button.type = 'button'
      button.dataset.action = action
      button.title = action
      button.setAttribute('aria-label', action)
      button.append(element('span', 'win-btn-glyph'))
      button.addEventListener('click', event => {
        event.stopPropagation()
        this.#onAction(action)
      })
      this.#header.append(button)
    }

    this.#header.addEventListener('pointerdown', event => this.#beginDrag(event))
    this.#header.addEventListener('dblclick', () => this.toggleMaximize())
    this.el.addEventListener('pointerdown', () => this.focus())

    this.el.append(this.#header, this.body)

    if (resizable) {
      const grip = element('div', 'win-grip')
      grip.addEventListener('pointerdown', event => this.#beginResize(event))
      this.el.append(grip)
    }
  }

  #onAction(action: 'minimize' | 'maximize' | 'close'): void {
    if (action === 'minimize') {
      this.minimize()
    } else if (action === 'maximize') {
      this.toggleMaximize()
    } else {
      this.close()
    }
  }

  #renderTitle(): void {
    this.#titleEl.textContent = this.#title
    this.el.setAttribute('aria-label', this.#title)
  }

  /**
   * Pointer capture keeps the gesture on the grabbed element, so a fast drag
   * that outruns the cursor does not drop the window.
   */
  #drag(
    event: PointerEvent,
    state: string,
    onMove: (dx: number, dy: number) => void
  ): void {
    if (event.button !== 0) {
      return
    }
    event.preventDefault()

    const target = event.currentTarget as HTMLElement
    const startX = event.clientX
    const startY = event.clientY

    target.setPointerCapture(event.pointerId)
    this.el.dataset[state] = 'true'

    const move = (moved: PointerEvent) => onMove(moved.clientX - startX, moved.clientY - startY)

    const end = (ended: PointerEvent) => {
      target.releasePointerCapture(ended.pointerId)
      target.removeEventListener('pointermove', move)
      target.removeEventListener('pointerup', end)
      target.removeEventListener('pointercancel', end)
      delete this.el.dataset[state]
    }

    target.addEventListener('pointermove', move)
    target.addEventListener('pointerup', end)
    target.addEventListener('pointercancel', end)
  }

  #beginDrag(event: PointerEvent): void {
    if (this.maximized || (event.target as HTMLElement).closest('.win-btn')) {
      return
    }
    const origin = this.rect
    this.focus()
    this.#drag(event, 'moving', (dx, dy) => this.moveTo(origin.x + dx, origin.y + dy))
  }

  #beginResize(event: PointerEvent): void {
    if (this.maximized) {
      return
    }
    const origin = this.rect
    this.focus()
    this.#drag(event, 'resizing', (dx, dy) =>
      this.resizeTo(origin.width + dx, origin.height + dy)
    )
  }

  #applyRect(): void {
    const {x, y, width, height} = this.#rect
    this.el.style.transform = `translate(${x}px, ${y}px)`
    this.el.style.width = `${width}px`
    this.el.style.height = `${height}px`
  }
}
