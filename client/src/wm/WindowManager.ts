/**
 * Owns the window list, stacking order and where a new window lands.
 *
 * The workspace rect is injected rather than measured, so the manager has no
 * opinion about which element the desktop is and stays testable without layout.
 */

import {Bus} from '../core/bus'
import {Window, type WindowOptions} from './Window'

export interface Rect {
  x: number
  y: number
  width: number
  height: number
}

type WindowEvents = {
  open: Window
  close: Window
  focus: Window | null
  update: Window
}

const BASE_Z = 10
const CASCADE_STEP = 26
const CASCADE_LIMIT = 8

export class WindowManager {
  readonly bus = new Bus<WindowEvents>()

  readonly #root: HTMLElement
  readonly #measure: () => Rect
  readonly #windows: Window[] = []

  #z = BASE_Z
  #cascade = 0
  #focused: Window | null = null

  constructor(root: HTMLElement, measure?: () => Rect) {
    this.#root = root
    this.#measure = measure ?? (() => ({
      x: 0,
      y: 0,
      width: root.clientWidth,
      height: root.clientHeight
    }))
  }

  get workspace(): Rect {
    return this.#measure()
  }

  get windows(): readonly Window[] {
    return this.#windows
  }

  get focused(): Window | null {
    return this.#focused
  }

  open(options: WindowOptions): Window {
    const window = new Window(this, options, this.#nextPosition(options))
    this.#windows.push(window)
    this.#root.append(window.el)
    this.bus.emit('open', window)
    this.focus(window)
    return window
  }

  focus(window: Window): void {
    if (!this.#windows.includes(window) || window.minimized) {
      return
    }

    this.#focused?.setFocused(false)
    this.#focused = window
    window.setFocused(true)
    window.setZIndex(++this.#z)
    this.bus.emit('focus', window)
  }

  /** Focus the highest window still on screen, after a close or a minimize. */
  focusTopmost(): void {
    const candidates = this.#windows.filter(window => !window.minimized)
    const next = candidates[candidates.length - 1]

    if (next === undefined) {
      this.#focused?.setFocused(false)
      this.#focused = null
      this.bus.emit('focus', null)
      return
    }

    this.focus(next)
  }

  close(window: Window): void {
    const index = this.#windows.indexOf(window)
    if (index === -1) {
      return
    }

    this.#windows.splice(index, 1)
    window.el.remove()

    if (this.#focused === window) {
      this.#focused = null
      this.focusTopmost()
    }

    this.bus.emit('close', window)
  }

  /** Cascade from the workspace origin, wrapping before windows march off it. */
  #nextPosition(options: WindowOptions): {x: number; y: number} {
    const space = this.workspace
    const offset = (this.#cascade % CASCADE_LIMIT) * CASCADE_STEP
    this.#cascade += 1

    const width = options.width ?? 640
    const height = options.height ?? 420

    return {
      x: space.x + Math.min(offset, Math.max(space.width - width, 0)),
      y: space.y + Math.min(offset, Math.max(space.height - height, 0))
    }
  }
}
