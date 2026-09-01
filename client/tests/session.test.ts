import {afterEach, beforeEach, describe, expect, it, vi} from 'vitest'
import {Session} from '../src/core/session'

const ok = (body: unknown): Response =>
  ({ok: true, status: 200, statusText: 'OK', json: () => Promise.resolve(body)}) as unknown as Response

const forbidden = (): Response =>
  ({
    ok: false,
    status: 403,
    statusText: 'FORBIDDEN',
    json: () => Promise.resolve({error: 'Not authenticated'})
  }) as unknown as Response

let fetchMock: ReturnType<typeof vi.fn>

const bodyOf = (index: number): unknown =>
  JSON.parse(String((fetchMock.mock.calls[index] as [string, RequestInit])[1].body))

beforeEach(() => {
  window.localStorage.clear()
  fetchMock = vi.fn()
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('restoring a session', () => {
  it('reports no session when the server answers 403', async () => {
    fetchMock.mockResolvedValue(forbidden())

    await expect(new Session().restore()).resolves.toBe(false)
  })

  it('recovers the profile stored at login', async () => {
    window.localStorage.setItem(
      'minos.profile',
      JSON.stringify({id: 'demo', username: 'demo', name: 'demo', groups: []})
    )
    fetchMock.mockResolvedValue(ok({}))

    const session = new Session()
    await expect(session.restore()).resolves.toBe(true)
    expect(session.username).toBe('demo')
  })

  it('still restores when local storage was cleared', async () => {
    fetchMock.mockResolvedValue(ok({}))

    const session = new Session()
    await expect(session.restore()).resolves.toBe(true)
    expect(session.username).toBe('unknown')
  })
})

describe('settings', () => {
  it('keeps the other client keys when writing our own', async () => {
    // The OS.js client stores its own keys in the same settings file.
    const existing = {
      'osjs/desktop': {theme: 'MonoBlueTheme'},
      'osjs/session': [{name: 'Textpad'}]
    }
    fetchMock.mockResolvedValueOnce(ok(existing)).mockResolvedValueOnce(ok(true))

    const session = new Session()
    await session.restore()
    await session.patchDesktop({lastPath: 'home:/notes'})

    expect(bodyOf(1)).toEqual({
      ...existing,
      'minos/desktop': {lastPath: 'home:/notes'}
    })
  })

  it('merges into our namespace rather than replacing it', async () => {
    fetchMock
      .mockResolvedValueOnce(ok({'minos/desktop': {lastPath: 'home:/', other: 1}}))
      .mockResolvedValueOnce(ok(true))

    const session = new Session()
    await session.restore()
    await session.patchDesktop({lastPath: 'home:/notes'})

    expect(bodyOf(1)).toEqual({'minos/desktop': {lastPath: 'home:/notes', other: 1}})
  })

  it('reads back an empty object before anything is saved', async () => {
    fetchMock.mockResolvedValue(ok({}))

    const session = new Session()
    await session.restore()

    expect(session.desktop).toEqual({})
  })
})
