/**
 * HTTP client for the minos server.
 *
 * The request shapes here are fixed by server/app.py and are the OS.js wire
 * format: reads are GETs carrying JSON-encoded query parameters, writes are
 * JSON POSTs, and writefile is multipart. This module is the only place that
 * knows any of that.
 *
 * Paths are absolute so they resolve against the server root no matter where
 * the bundle is mounted.
 */

export interface UserProfile {
  id: string
  username: string
  name: string
  groups: string[]
}

export interface FileStat {
  size: number
  mode: number
  atime: string
  mtime: string
  ctime: string
  atimeMs: number
  mtimeMs: number
  ctimeMs: number
}

export interface FileEntry {
  isDirectory: boolean
  isFile: boolean
  mime: string | null
  size: number
  path: string
  filename: string
  stat: FileStat
}

export type Settings = Record<string, unknown>

export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number
  ) {
    super(message)
    this.name = 'ApiError'
  }

  get isUnauthenticated(): boolean {
    return this.status === 403
  }
}

const request = async (path: string, init: RequestInit = {}): Promise<Response> => {
  const response = await fetch(path, {credentials: 'same-origin', ...init})
  if (response.ok) {
    return response
  }

  // Errors come back as {"error": "..."}; fall back to the status line when a
  // proxy or a crash returns something else.
  const message = await response
    .json()
    .then((body: unknown) =>
      typeof body === 'object' && body !== null && 'error' in body ? String(body.error) : null
    )
    .catch(() => null)

  throw new ApiError(message ?? `${response.status} ${response.statusText}`, response.status)
}

const query = (params: Record<string, string | undefined>): string => {
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined) {
      search.set(key, value)
    }
  }
  return search.toString()
}

const getJson = <T>(path: string, params: Record<string, string | undefined>): Promise<T> =>
  request(`${path}?${query(params)}`).then(response => response.json() as Promise<T>)

const postJson = <T>(path: string, body: unknown): Promise<T> =>
  request(path, {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify(body)
  }).then(response => response.json() as Promise<T>)

export const api = {
  ping: (): Promise<void> => request('/ping').then(() => undefined),

  login: (username: string, password: string): Promise<UserProfile> =>
    postJson<UserProfile>('/login', {username, password}),

  logout: (): Promise<void> => postJson<unknown>('/logout', {}).then(() => undefined),

  loadSettings: (): Promise<Settings> => request('/settings').then(r => r.json() as Promise<Settings>),

  saveSettings: (settings: Settings): Promise<void> =>
    postJson<boolean>('/settings', settings).then(() => undefined),

  readdir: (path: string): Promise<FileEntry[]> => getJson<FileEntry[]>('/vfs/readdir', {path}),

  stat: (path: string): Promise<FileEntry> => getJson<FileEntry>('/vfs/stat', {path}),

  exists: (path: string): Promise<boolean> => getJson<boolean>('/vfs/exists', {path}),

  readfile: (path: string): Promise<Blob> =>
    request(`/vfs/readfile?${query({path})}`).then(response => response.blob()),

  readtext: (path: string): Promise<string> =>
    request(`/vfs/readfile?${query({path})}`).then(response => response.text()),

  /** Resolves to the number of bytes stored. */
  writefile: (path: string, data: Blob | File): Promise<number> => {
    const form = new FormData()
    form.append('path', path)
    form.append('upload', data)
    return request('/vfs/writefile', {method: 'POST', body: form}).then(
      response => response.json() as Promise<number>
    )
  },

  mkdir: (path: string): Promise<boolean> => postJson<boolean>('/vfs/mkdir', {path}),

  unlink: (path: string): Promise<boolean> => postJson<boolean>('/vfs/unlink', {path}),

  touch: (path: string): Promise<boolean> => postJson<boolean>('/vfs/touch', {path}),

  rename: (from: string, to: string): Promise<boolean> =>
    postJson<boolean>('/vfs/rename', {from, to}),

  copy: (from: string, to: string): Promise<boolean> => postJson<boolean>('/vfs/copy', {from, to}),

  search: (root: string, pattern: string): Promise<FileEntry[]> =>
    postJson<FileEntry[]>('/vfs/search', {root, pattern}),

  /** A URL the browser can load directly, for previews and downloads. */
  fileUrl: (path: string, download = false): string =>
    `/vfs/readfile?${query({
      path,
      options: download ? JSON.stringify({download: true}) : undefined
    })}`
}
