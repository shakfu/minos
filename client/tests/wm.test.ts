import {beforeEach, describe, expect, it} from 'vitest'
import {WindowManager, type Rect} from '../src/wm/WindowManager'

const WORKSPACE: Rect = {x: 0, y: 0, width: 1000, height: 600}

let root: HTMLElement
let wm: WindowManager

const zIndex = (element: HTMLElement): number => Number(element.style.zIndex)

beforeEach(() => {
  document.body.replaceChildren()
  root = document.createElement('div')
  document.body.append(root)
  wm = new WindowManager(root, () => WORKSPACE)
})

describe('opening', () => {
  it('mounts the window and focuses it', () => {
    const window_ = wm.open({title: 'One'})

    expect(root.contains(window_.el)).toBe(true)
    expect(wm.focused).toBe(window_)
    expect(window_.el.dataset.focused).toBe('true')
  })

  it('cascades so a new window does not land exactly on the last', () => {
    const first = wm.open({title: 'One'})
    const second = wm.open({title: 'Two'})

    expect(second.rect.x).toBeGreaterThan(first.rect.x)
    expect(second.rect.y).toBeGreaterThan(first.rect.y)
  })

  it('keeps a cascaded window inside the workspace', () => {
    const windows = Array.from({length: 12}, (_, index) =>
      wm.open({title: `W${index}`, width: 640, height: 420})
    )

    for (const window_ of windows) {
      expect(window_.rect.x).toBeLessThanOrEqual(WORKSPACE.width - 640)
      expect(window_.rect.y).toBeLessThanOrEqual(WORKSPACE.height - 420)
    }
  })
})

describe('stacking', () => {
  it('raises a window above the others when focused', () => {
    const first = wm.open({title: 'One'})
    const second = wm.open({title: 'Two'})

    expect(zIndex(second.el)).toBeGreaterThan(zIndex(first.el))

    first.focus()

    expect(zIndex(first.el)).toBeGreaterThan(zIndex(second.el))
    expect(wm.focused).toBe(first)
    expect(second.el.dataset.focused).toBe('false')
  })

  it('ignores a focus request for a window it does not own', () => {
    const orphan = wm.open({title: 'Orphan'})
    wm.close(orphan)
    const other = wm.open({title: 'Other'})

    orphan.focus()

    expect(wm.focused).toBe(other)
  })
})

describe('closing', () => {
  it('unmounts and focuses whatever is left', () => {
    const first = wm.open({title: 'One'})
    const second = wm.open({title: 'Two'})

    second.close()

    expect(root.contains(second.el)).toBe(false)
    expect(wm.windows).toEqual([first])
    expect(wm.focused).toBe(first)
  })

  it('leaves nothing focused once the last window goes', () => {
    wm.open({title: 'Only'}).close()

    expect(wm.windows).toHaveLength(0)
    expect(wm.focused).toBeNull()
  })

  it('is safe to call twice', () => {
    const window_ = wm.open({title: 'One'})
    window_.close()
    window_.close()

    expect(wm.windows).toHaveLength(0)
  })
})

describe('minimizing', () => {
  it('hides the window and moves focus on', () => {
    const first = wm.open({title: 'One'})
    const second = wm.open({title: 'Two'})

    second.minimize()

    expect(second.minimized).toBe(true)
    expect(second.el.hidden).toBe(true)
    expect(wm.focused).toBe(first)
  })

  it('restores and takes focus back', () => {
    const first = wm.open({title: 'One'})
    first.minimize()
    first.restore()

    expect(first.minimized).toBe(false)
    expect(first.el.hidden).toBe(false)
    expect(wm.focused).toBe(first)
  })

  it('will not focus a minimized window', () => {
    const first = wm.open({title: 'One'})
    const second = wm.open({title: 'Two'})
    second.minimize()

    second.focus()

    expect(wm.focused).toBe(first)
  })
})

describe('geometry', () => {
  it('keeps a dragged window reachable on the left and top', () => {
    const window_ = wm.open({title: 'One', width: 640, height: 420})

    window_.moveTo(-9999, -9999)

    expect(window_.rect.x).toBe(80 - 640)
    expect(window_.rect.y).toBe(0)
  })

  it('keeps a dragged window reachable on the right and bottom', () => {
    const window_ = wm.open({title: 'One', width: 640, height: 420})

    window_.moveTo(9999, 9999)

    expect(window_.rect.x).toBe(WORKSPACE.width - 80)
    expect(window_.rect.y).toBe(WORKSPACE.height - 28)
  })

  it('refuses to shrink below the minimum size', () => {
    const window_ = wm.open({title: 'One', minWidth: 300, minHeight: 200})

    window_.resizeTo(10, 10)

    expect(window_.rect).toMatchObject({width: 300, height: 200})
  })

  it('fills the workspace when maximized and returns to where it was', () => {
    const window_ = wm.open({title: 'One', width: 400, height: 300})
    window_.moveTo(120, 90)
    const before = window_.rect

    window_.toggleMaximize()

    expect(window_.maximized).toBe(true)
    expect(window_.rect).toEqual(WORKSPACE)

    window_.toggleMaximize()

    expect(window_.maximized).toBe(false)
    expect(window_.rect).toEqual(before)
  })

  it('writes geometry as a transform rather than layout offsets', () => {
    const window_ = wm.open({title: 'One', width: 400, height: 300})
    window_.moveTo(120, 90)

    expect(window_.el.style.transform).toBe('translate(120px, 90px)')
    expect(window_.el.style.width).toBe('400px')
  })
})

describe('notifications', () => {
  it('announces open, focus, update and close', () => {
    const seen: string[] = []
    for (const event of ['open', 'focus', 'update', 'close'] as const) {
      wm.bus.on(event, () => seen.push(event))
    }

    const window_ = wm.open({title: 'One'})
    window_.setTitle('Renamed')
    window_.close()

    expect(seen).toEqual(['open', 'focus', 'update', 'focus', 'close'])
  })

  it('carries the new title', () => {
    const window_ = wm.open({title: 'One'})
    window_.setTitle('Files - home:/')

    expect(window_.title).toBe('Files - home:/')
    expect(window_.el.textContent).toContain('Files - home:/')
  })
})
