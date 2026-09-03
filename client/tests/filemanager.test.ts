/**
 * Drives the file manager against a stubbed server and reads the DOM back.
 */

import {afterEach, beforeEach, describe, expect, it, vi} from 'vitest'
import {openFileManager} from '../src/apps/FileManager'
import type {FileEntry} from '../src/core/api'
import type {ChatClient} from '../src/core/chat'
import {Session} from '../src/core/session'
import {WindowManager} from '../src/wm/WindowManager'

const entry = (filename: string, isDirectory: boolean, size = 0): FileEntry => ({
  isDirectory,
  isFile: !isDirectory,
  mime: isDirectory ? null : 'text/plain',
  size,
  path: `home:/${filename}`,
  filename,
  stat: {
    size,
    mode: 0,
    atime: '2026-09-01T08:00:00+00:00',
    mtime: '2026-09-01T08:00:00+00:00',
    ctime: '2026-09-01T08:00:00+00:00',
    atimeMs: 0,
    mtimeMs: 0,
    ctimeMs: 0
  }
})

const HOME = [entry('notes', true), entry('readme.txt', false, 2048)]

let root: HTMLElement
let listings: Map<string, FileEntry[]>

const json = (body: unknown): Response =>
  ({ok: true, status: 200, statusText: 'OK', json: () => Promise.resolve(body)}) as unknown as Response

beforeEach(() => {
  document.body.replaceChildren()
  root = document.createElement('div')
  document.body.append(root)

  listings = new Map([
    ['home:/', HOME],
    ['home:/notes', [entry('todo.md', false, 10)]],
    ['osjs:/', [entry('index.html', false, 500)]]
  ])

  vi.stubGlobal(
    'fetch',
    vi.fn((url: string) => {
      if (url.startsWith('/vfs/readdir')) {
        const path = new URL(url, 'http://x').searchParams.get('path') ?? ''
        return Promise.resolve(json(listings.get(path) ?? []))
      }
      return Promise.resolve(json({}))
    })
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
})

// The file manager never opens a conversation; the context type just
// requires a client, so a stub stands in for one.
const chat = {} as ChatClient

const start = async (): Promise<HTMLElement> => {
  const wm = new WindowManager(root, () => ({x: 0, y: 0, width: 1000, height: 600}))
  const session = new Session()
  await session.restore()

  openFileManager({wm, session, chat})

  const body = root.querySelector<HTMLElement>('.files')
  if (body === null) {
    throw new Error('file manager did not mount')
  }
  await vi.waitFor(() => expect(body.querySelectorAll('.files-row').length).toBeGreaterThan(0))
  return body
}

const rowNames = (body: HTMLElement): string[] =>
  Array.from(body.querySelectorAll('.files-row .col-name')).map(cell => cell.textContent ?? '')

describe('listing a directory', () => {
  it('renders a row per entry, directories first and marked', async () => {
    const body = await start()

    expect(rowNames(body)).toEqual(['notes/', 'readme.txt'])
    expect(body.querySelectorAll('[data-kind="dir"]')).toHaveLength(1)
  })

  it('shows human sizes for files and nothing for directories', async () => {
    const body = await start()
    const sizes = Array.from(body.querySelectorAll('.files-row .col-size')).map(
      cell => cell.textContent
    )

    expect(sizes).toEqual(['', '2.0 KB'])
  })

  it('summarises the directory in the status bar', async () => {
    const body = await start()

    expect(body.querySelector('.files-status')?.textContent).toBe('1 folder(s), 1 file(s)')
  })

  it('builds a breadcrumb from the mountpoint', async () => {
    const body = await start()
    const crumbs = Array.from(body.querySelectorAll('.files-crumb')).map(c => c.textContent)

    expect(crumbs).toEqual(['Home'])
  })
})

describe('navigating', () => {
  it('opens a directory on double click and updates the trail', async () => {
    const body = await start()

    body.querySelector<HTMLElement>('.files-row')?.dispatchEvent(
      new window.MouseEvent('dblclick', {bubbles: true})
    )

    await vi.waitFor(() => expect(rowNames(body)).toEqual(['todo.md']))
    expect(Array.from(body.querySelectorAll('.files-crumb')).map(c => c.textContent)).toEqual([
      'Home',
      'notes'
    ])
  })

  it('puts the current path in the window title', async () => {
    const body = await start()
    const title = body.closest('.win')?.querySelector('.win-title')

    await vi.waitFor(() => expect(title?.textContent).toBe('Files - home:/'))
  })

  it('remembers the directory through the settings endpoint', async () => {
    await start()
    const calls = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls as [string, RequestInit][]
    const saved = calls.filter(([url, init]) => url === '/settings' && init?.method === 'POST')

    expect(JSON.parse(String(saved[0]?.[1].body))).toEqual({'minos/desktop': {lastPath: 'home:/'}})
  })
})

describe('read-only mountpoints', () => {
  it('disables the write actions and says so', async () => {
    const body = await start()
    const system = Array.from(body.querySelectorAll<HTMLButtonElement>('.btn')).find(
      button => button.textContent === 'System'
    )

    system?.click()

    await vi.waitFor(() => expect(rowNames(body)).toEqual(['index.html']))

    const labels = Array.from(body.querySelectorAll<HTMLButtonElement>('.btn'))
      .filter(button => button.disabled)
      .map(button => button.textContent)

    expect(labels).toEqual(['New folder', 'Upload'])
    expect(body.querySelector('.files-status')?.textContent).toContain('read only')
  })
})

describe('selection', () => {
  it('marks the clicked row and only that row', async () => {
    const body = await start()

    body.querySelectorAll<HTMLElement>('.files-row')[1]?.click()

    await vi.waitFor(() => {
      const selected = body.querySelectorAll('[data-selected="true"]')
      expect(selected).toHaveLength(1)
      expect(selected[0]?.textContent).toContain('readme.txt')
    })
  })
})

describe('an empty directory', () => {
  it('says so instead of rendering an empty table', async () => {
    listings.set('home:/', [])
    const wm = new WindowManager(root, () => ({x: 0, y: 0, width: 1000, height: 600}))
    const session = new Session()
    await session.restore()

    openFileManager({wm, session, chat})

    const body = root.querySelector<HTMLElement>('.files')
    await vi.waitFor(() => expect(body?.querySelector('.files-empty')?.textContent).toBe('Nothing here'))
  })
})
