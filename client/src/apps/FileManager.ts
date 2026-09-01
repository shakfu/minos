/**
 * The file manager, and the only real consumer of the /vfs API.
 *
 * Listings are re-rendered wholesale after every mutation rather than patched:
 * the server is the source of truth for names, sizes and timestamps, and a
 * directory is small enough that rebuilding a table is not worth avoiding.
 */

import {api, ApiError, type FileEntry} from '../core/api'
import * as vfs from '../core/vfspath'
import {alert as showAlert, confirm as showConfirm, prompt as showPrompt} from '../ui/Dialog'
import {SEPARATOR, showMenu} from '../ui/Menu'
import type {Window} from '../wm/Window'
import type {AppContext} from './index'
import {download, openViewer} from './Viewer'

const DEFAULT_PATH = vfs.root('home')

export const openFileManager = (context: AppContext): void => {
  const window_ = context.wm.open({
    title: 'Files',
    width: 760,
    height: 480,
    minWidth: 440,
    minHeight: 280
  })

  const manager = new FileManager(window_, context)
  void manager.start()
}

class FileManager {
  readonly #window: Window
  readonly #context: AppContext

  readonly #trail = document.createElement('nav')
  readonly #tbody = document.createElement('tbody')
  readonly #status = document.createElement('div')
  readonly #search = document.createElement('input')
  readonly #upload = document.createElement('input')
  readonly #writeButtons: HTMLButtonElement[] = []

  #path = DEFAULT_PATH
  #entries: FileEntry[] = []
  #selected: string | null = null

  constructor(window_: Window, context: AppContext) {
    this.#window = window_
    this.#context = context
    this.#build()
  }

  async start(): Promise<void> {
    const remembered = this.#context.session.desktop.lastPath
    await this.#navigate(remembered ?? DEFAULT_PATH)
  }

  // -- construction ---------------------------------------------------------

  #build(): void {
    this.#window.body.classList.add('files')
    this.#window.body.append(this.#toolbar(), this.#trailBar(), this.#table(), this.#statusBar())

    this.#upload.type = 'file'
    this.#upload.multiple = true
    this.#upload.hidden = true
    this.#upload.addEventListener('change', () => {
      void this.#uploadFiles(Array.from(this.#upload.files ?? []))
      this.#upload.value = ''
    })
    this.#window.body.append(this.#upload)

    this.#window.body.addEventListener('dragover', event => {
      event.preventDefault()
      this.#window.body.dataset.dropping = 'true'
    })
    this.#window.body.addEventListener('dragleave', () => {
      delete this.#window.body.dataset.dropping
    })
    this.#window.body.addEventListener('drop', event => {
      event.preventDefault()
      delete this.#window.body.dataset.dropping
      void this.#uploadFiles(Array.from(event.dataTransfer?.files ?? []))
    })
  }

  #toolbar(): HTMLElement {
    const bar = document.createElement('div')
    bar.className = 'files-toolbar'

    bar.append(
      this.#button('Up', () => void this.#navigate(vfs.parent(this.#path))),
      this.#button('Refresh', () => void this.#refresh()),
      this.#button('New folder', () => void this.#createFolder(), true),
      this.#button('Upload', () => this.#upload.click(), true)
    )

    for (const mount of vfs.MOUNTPOINTS) {
      bar.append(this.#button(mount.label, () => void this.#navigate(vfs.root(mount.name))))
    }

    this.#search.type = 'search'
    this.#search.className = 'files-search'
    this.#search.placeholder = 'Search'
    this.#search.addEventListener('keydown', event => {
      if (event.key === 'Enter') {
        void this.#runSearch()
      }
    })
    bar.append(this.#search)

    return bar
  }

  #button(label: string, onClick: () => void, needsWrite = false): HTMLButtonElement {
    const button = document.createElement('button')
    button.type = 'button'
    button.className = 'btn'
    button.textContent = label
    button.addEventListener('click', onClick)
    if (needsWrite) {
      this.#writeButtons.push(button)
    }
    return button
  }

  #trailBar(): HTMLElement {
    this.#trail.className = 'files-trail'
    return this.#trail
  }

  #table(): HTMLElement {
    const wrapper = document.createElement('div')
    wrapper.className = 'files-list'

    const table = document.createElement('table')
    const head = document.createElement('thead')
    const row = document.createElement('tr')

    for (const [label, className] of [
      ['Name', 'col-name'],
      ['Size', 'col-size'],
      ['Modified', 'col-time']
    ] as const) {
      const cell = document.createElement('th')
      cell.textContent = label
      cell.className = className
      row.append(cell)
    }

    head.append(row)
    table.append(head, this.#tbody)
    wrapper.append(table)
    return wrapper
  }

  #statusBar(): HTMLElement {
    this.#status.className = 'files-status'
    return this.#status
  }

  // -- data -----------------------------------------------------------------

  async #navigate(path: string): Promise<void> {
    const entries = await this.#guard(api.readdir(path))
    if (entries === null) {
      return
    }

    this.#path = path
    this.#entries = sortEntries(entries)
    this.#selected = null
    this.#search.value = ''
    this.#render()

    this.#window.setTitle(`Files - ${path}`)
    void this.#context.session.patchDesktop({lastPath: path}).catch(() => undefined)
  }

  async #refresh(): Promise<void> {
    await this.#navigate(this.#path)
  }

  async #runSearch(): Promise<void> {
    const pattern = this.#search.value.trim()
    if (pattern === '') {
      await this.#refresh()
      return
    }

    const results = await this.#guard(api.search(this.#path, pattern))
    if (results === null) {
      return
    }

    this.#entries = sortEntries(results)
    this.#selected = null
    this.#render()
    this.#status.textContent = `${results.length} match(es) for "${pattern}" under ${this.#path}`
  }

  async #uploadFiles(files: File[]): Promise<void> {
    if (files.length === 0 || vfs.isReadOnly(this.#path)) {
      return
    }

    for (const file of files) {
      const written = await this.#guard(api.writefile(vfs.join(this.#path, file.name), file))
      if (written === null) {
        break
      }
    }
    await this.#refresh()
  }

  async #createFolder(): Promise<void> {
    const name = await showPrompt('Create a folder in ' + this.#path, 'untitled', 'Name')
    if (name === null) {
      return
    }
    if ((await this.#guard(api.mkdir(vfs.join(this.#path, name)))) !== null) {
      await this.#refresh()
    }
  }

  async #rename(entry: FileEntry): Promise<void> {
    const name = await showPrompt(`Rename "${entry.filename}"`, entry.filename, 'Name')
    if (name === null || name === entry.filename) {
      return
    }
    const target = vfs.join(vfs.parent(entry.path), name)
    if ((await this.#guard(api.rename(entry.path, target))) !== null) {
      await this.#refresh()
    }
  }

  async #remove(entry: FileEntry): Promise<void> {
    const what = entry.isDirectory ? 'folder and everything in it' : 'file'
    if (!(await showConfirm(`Delete the ${what} "${entry.filename}"?`, 'Delete'))) {
      return
    }
    if ((await this.#guard(api.unlink(entry.path))) !== null) {
      await this.#refresh()
    }
  }

  #open(entry: FileEntry): void {
    if (entry.isDirectory) {
      void this.#navigate(entry.path)
    } else {
      openViewer(this.#context, entry)
    }
  }

  /**
   * Resolve to null instead of throwing, so callers can bail quietly. A 403
   * means the server session is gone and nothing else will work either.
   */
  async #guard<T>(work: Promise<T>): Promise<T | null> {
    try {
      return await work
    } catch (cause) {
      if (cause instanceof ApiError && cause.isUnauthenticated) {
        await showAlert('Your session has expired. Reloading.')
        window.location.reload()
        return null
      }
      await showAlert(cause instanceof Error ? cause.message : String(cause))
      return null
    }
  }

  // -- rendering ------------------------------------------------------------

  #render(): void {
    this.#renderTrail()
    this.#renderRows()

    const readOnly = vfs.isReadOnly(this.#path)
    for (const button of this.#writeButtons) {
      button.disabled = readOnly
    }

    const folders = this.#entries.filter(entry => entry.isDirectory).length
    this.#status.textContent =
      `${folders} folder(s), ${this.#entries.length - folders} file(s)` +
      (readOnly ? ' - read only' : '')
  }

  #renderTrail(): void {
    this.#trail.replaceChildren(
      ...vfs.trail(this.#path).map(crumb => {
        const button = document.createElement('button')
        button.type = 'button'
        button.className = 'files-crumb'
        button.textContent = crumb.label
        button.addEventListener('click', () => void this.#navigate(crumb.path))
        return button
      })
    )
  }

  #renderRows(): void {
    if (this.#entries.length === 0) {
      const row = document.createElement('tr')
      const cell = document.createElement('td')
      cell.colSpan = 3
      cell.className = 'files-empty'
      cell.textContent = 'Nothing here'
      row.append(cell)
      this.#tbody.replaceChildren(row)
      return
    }

    this.#tbody.replaceChildren(...this.#entries.map(entry => this.#row(entry)))
  }

  #row(entry: FileEntry): HTMLTableRowElement {
    const row = document.createElement('tr')
    row.className = 'files-row'
    row.dataset.kind = entry.isDirectory ? 'dir' : 'file'
    row.dataset.selected = String(this.#selected === entry.path)

    row.append(
      cell(entry.isDirectory ? `${entry.filename}/` : entry.filename, 'col-name'),
      cell(entry.isDirectory ? '' : vfs.formatSize(entry.size), 'col-size'),
      cell(formatTime(entry.stat.mtime), 'col-time')
    )

    row.addEventListener('click', () => {
      this.#selected = entry.path
      this.#renderRows()
    })
    row.addEventListener('dblclick', () => this.#open(entry))
    row.addEventListener('contextmenu', event => {
      event.preventDefault()
      this.#selected = entry.path
      this.#renderRows()
      this.#showRowMenu(entry, event)
    })

    return row
  }

  #showRowMenu(entry: FileEntry, event: MouseEvent): void {
    const readOnly = vfs.isReadOnly(this.#path)

    showMenu(
      [
        {label: entry.isDirectory ? 'Open' : 'View', onSelect: () => this.#open(entry)},
        {
          label: 'Download',
          disabled: entry.isDirectory,
          onSelect: () => download(entry)
        },
        SEPARATOR,
        {label: 'Rename', disabled: readOnly, onSelect: () => void this.#rename(entry)},
        {label: 'Delete', disabled: readOnly, onSelect: () => void this.#remove(entry)}
      ],
      {x: event.clientX, y: event.clientY}
    )
  }
}

const cell = (text: string, className: string): HTMLTableCellElement => {
  const node = document.createElement('td')
  node.className = className
  node.textContent = text
  node.title = text
  return node
}

/** Directories first, then by name. Matches what every file manager does. */
export const sortEntries = (entries: readonly FileEntry[]): FileEntry[] =>
  [...entries].sort((a, b) => {
    if (a.isDirectory !== b.isDirectory) {
      return a.isDirectory ? -1 : 1
    }
    return a.filename.localeCompare(b.filename, undefined, {sensitivity: 'base'})
  })

export const formatTime = (iso: string): string => {
  const date = new Date(iso)
  return Number.isNaN(date.getTime())
    ? ''
    : new Intl.DateTimeFormat(undefined, {dateStyle: 'short', timeStyle: 'short'}).format(date)
}
