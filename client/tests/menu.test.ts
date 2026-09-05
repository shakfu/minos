/**
 * The floating menu, and specifically how it gets dismissed.
 *
 * The dismissal listeners are the subtle part: registered with `once` they were
 * removed by the first event of their type rather than the first one that
 * actually dismissed, so a stray click or keypress could leave a menu with no
 * way to close.
 */

import {beforeEach, describe, expect, it, vi} from 'vitest'

import {closeMenu, SEPARATOR, showMenu} from '../src/ui/Menu'

const at = {x: 10, y: 10}

const menu = (): HTMLElement | null => document.querySelector('.menu')
const isOpen = (): boolean => menu() !== null

const items = (): HTMLButtonElement[] =>
  Array.from(document.querySelectorAll<HTMLButtonElement>('.menu-item'))

/** A pointerdown that bubbles to window, as a real one does. */
const pointerDownOn = (target: EventTarget): void => {
  target.dispatchEvent(new Event('pointerdown', {bubbles: true}))
}

const keyDown = (key: string): void => {
  window.dispatchEvent(new KeyboardEvent('keydown', {key, bubbles: true}))
}

beforeEach(() => {
  closeMenu()
  document.body.replaceChildren()
  vi.useFakeTimers()
})

/** The listeners are attached on a deferred tick, so let it run. */
const settle = (): void => {
  vi.runAllTimers()
}

describe('showMenu', () => {
  it('renders its items and separators', () => {
    showMenu([{label: 'One'}, SEPARATOR, {label: 'Two', disabled: true}], at)
    settle()

    expect(items().map(i => i.textContent)).toEqual(['One', 'Two'])
    expect(items()[1]?.disabled).toBe(true)
    expect(document.querySelectorAll('.menu hr')).toHaveLength(1)
  })

  it('runs a selection and closes', () => {
    const chosen = vi.fn()
    showMenu([{label: 'Go', onSelect: chosen}], at)
    settle()

    items()[0]?.click()

    expect(chosen).toHaveBeenCalledOnce()
    expect(isOpen()).toBe(false)
  })

  it('closes on a click outside', () => {
    showMenu([{label: 'One'}], at)
    settle()

    pointerDownOn(document.body)

    expect(isOpen()).toBe(false)
  })

  it('closes on Escape', () => {
    showMenu([{label: 'One'}], at)
    settle()

    keyDown('Escape')

    expect(isOpen()).toBe(false)
  })

  it('replaces a menu that is already open', () => {
    showMenu([{label: 'First'}], at)
    settle()
    showMenu([{label: 'Second'}], at)
    settle()

    expect(document.querySelectorAll('.menu')).toHaveLength(1)
    expect(items().map(i => i.textContent)).toEqual(['Second'])
  })

  it('survives a click on its own body and still closes on the next outside one', () => {
    // A pointerdown inside the menu but not on an item -- its padding, or the
    // rule a separator draws. It must not dismiss, and it must not consume the
    // handler that dismisses.
    showMenu([{label: 'One'}, SEPARATOR, {label: 'Two'}], at)
    settle()

    const inside = document.querySelector('.menu hr')
    expect(inside).not.toBeNull()
    pointerDownOn(inside as Element)
    expect(isOpen()).toBe(true)

    pointerDownOn(document.body)
    expect(isOpen()).toBe(false)
  })

  it('still closes on Escape after another key was pressed', () => {
    showMenu([{label: 'One'}], at)
    settle()

    keyDown('a')
    expect(isOpen()).toBe(true)

    keyDown('Escape')
    expect(isOpen()).toBe(false)
  })

  it('stops listening once closed', () => {
    showMenu([{label: 'One'}], at)
    settle()
    closeMenu()

    // No menu is open, so these must be harmless rather than throwing on a
    // handler that outlived its menu.
    expect(() => {
      pointerDownOn(document.body)
      keyDown('Escape')
      window.dispatchEvent(new Event('blur'))
    }).not.toThrow()
    expect(isOpen()).toBe(false)
  })
})
