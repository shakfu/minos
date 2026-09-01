/**
 * These pin the wire format. server/ is frozen, so a change here is a bug on
 * this side by definition.
 */

import {afterEach, beforeEach, describe, expect, it, vi} from 'vitest'
import {ApiError, api} from '../src/core/api'

const ok = (body: unknown): Response =>
  ({
    ok: true,
    status: 200,
    statusText: 'OK',
    json: () => Promise.resolve(body),
    text: () => Promise.resolve(String(body)),
    blob: () => Promise.resolve(new Blob([String(body)]))
  }) as unknown as Response

const failure = (status: number, body: unknown): Response =>
  ({
    ok: false,
    status,
    statusText: 'Error',
    json: () =>
      body === undefined ? Promise.reject(new Error('not json')) : Promise.resolve(body)
  }) as unknown as Response

let fetchMock: ReturnType<typeof vi.fn>

const lastCall = (): [string, RequestInit] =>
  fetchMock.mock.calls[fetchMock.mock.calls.length - 1] as [string, RequestInit]

beforeEach(() => {
  fetchMock = vi.fn().mockResolvedValue(ok({}))
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('reads', () => {
  it('sends the path as a query parameter', async () => {
    await api.readdir('home:/notes')
    const [url, init] = lastCall()

    expect(url).toBe('/vfs/readdir?path=home%3A%2Fnotes')
    expect(init.method).toBeUndefined()
  })

  it('carries the session cookie', async () => {
    await api.stat('home:/a.txt')
    expect(lastCall()[1].credentials).toBe('same-origin')
  })

  it('uses the same endpoint for exists and capabilities style probes', async () => {
    await api.exists('home:/a.txt')
    expect(lastCall()[0]).toBe('/vfs/exists?path=home%3A%2Fa.txt')
  })
})

describe('writes', () => {
  it('posts JSON for mkdir, unlink and touch', async () => {
    await api.mkdir('home:/new')
    const [url, init] = lastCall()

    expect(url).toBe('/vfs/mkdir')
    expect(init.method).toBe('POST')
    expect((init.headers as Record<string, string>)['Content-Type']).toBe('application/json')
    expect(JSON.parse(String(init.body))).toEqual({path: 'home:/new'})
  })

  it('names the endpoints from and to for cross-path operations', async () => {
    await api.rename('home:/a', 'home:/b')
    expect(JSON.parse(String(lastCall()[1].body))).toEqual({from: 'home:/a', to: 'home:/b'})

    await api.copy('home:/a', 'home:/b')
    expect(lastCall()[0]).toBe('/vfs/copy')
  })

  it('sends search as root plus pattern', async () => {
    await api.search('home:/', 'report')
    const [url, init] = lastCall()

    expect(url).toBe('/vfs/search')
    expect(JSON.parse(String(init.body))).toEqual({root: 'home:/', pattern: 'report'})
  })

  it('uploads as multipart with upload and path fields', async () => {
    fetchMock.mockResolvedValue(ok(11))
    const file = new File(['hello world'], 'hello.txt', {type: 'text/plain'})

    await expect(api.writefile('home:/hello.txt', file)).resolves.toBe(11)

    const [url, init] = lastCall()
    expect(url).toBe('/vfs/writefile')
    expect(init.method).toBe('POST')

    const form = init.body as FormData
    expect(form.get('path')).toBe('home:/hello.txt')
    expect(form.get('upload')).toBeInstanceOf(File)
    // The server ignores a multipart Content-Type we set ourselves, so the
    // browser has to add the boundary.
    expect(init.headers).toBeUndefined()
  })
})

describe('direct URLs', () => {
  it('builds a plain readfile link', () => {
    expect(api.fileUrl('home:/a.txt')).toBe('/vfs/readfile?path=home%3A%2Fa.txt')
  })

  it('adds the download option as JSON when asked', () => {
    expect(api.fileUrl('home:/a.txt', true)).toBe(
      '/vfs/readfile?path=home%3A%2Fa.txt&options=%7B%22download%22%3Atrue%7D'
    )
  })
})

describe('auth and settings', () => {
  it('posts credentials as JSON', async () => {
    fetchMock.mockResolvedValue(ok({id: 'demo', username: 'demo', name: 'demo', groups: []}))
    const profile = await api.login('demo', 'demo')

    expect(lastCall()[0]).toBe('/login')
    expect(JSON.parse(String(lastCall()[1].body))).toEqual({username: 'demo', password: 'demo'})
    expect(profile.username).toBe('demo')
  })

  it('sends the whole settings object back', async () => {
    await api.saveSettings({'minos/desktop': {lastPath: 'home:/'}})
    expect(lastCall()[0]).toBe('/settings')
    expect(JSON.parse(String(lastCall()[1].body))).toEqual({'minos/desktop': {lastPath: 'home:/'}})
  })
})

describe('errors', () => {
  it('reports the server message', async () => {
    fetchMock.mockResolvedValue(failure(404, {error: 'No such file: home:/x'}))

    await expect(api.stat('home:/x')).rejects.toThrow('No such file: home:/x')
  })

  it('flags 403 so callers can tell an expired session apart', async () => {
    fetchMock.mockResolvedValue(failure(403, {error: 'Not authenticated'}))

    const error = await api.readdir('home:/').catch((cause: unknown) => cause)
    expect(error).toBeInstanceOf(ApiError)
    expect((error as ApiError).isUnauthenticated).toBe(true)
  })

  it('does not flag other failures as an expired session', async () => {
    fetchMock.mockResolvedValue(failure(409, {error: 'Already exists'}))

    const error = await api.mkdir('home:/dup').catch((cause: unknown) => cause)
    expect((error as ApiError).isUnauthenticated).toBe(false)
  })

  it('falls back to the status when the body is not JSON', async () => {
    fetchMock.mockResolvedValue(failure(502, undefined))

    await expect(api.readdir('home:/')).rejects.toThrow('502 Error')
  })
})
