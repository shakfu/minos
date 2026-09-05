/**
 * Reconnection policy.
 *
 * A fixed retry meant an expired session was hammered every few seconds for as
 * long as the tab stayed open, since the server closes an anonymous upgrade
 * rather than refusing it.
 */

import {afterEach, beforeEach, describe, expect, it, vi} from 'vitest'

import {ServerSocket} from '../src/core/socket'

/** A WebSocket stand-in that records instances and lets a test close them. */
class FakeWebSocket {
  static instances: FakeWebSocket[] = []
  static readonly OPEN = 1

  readyState = 0
  #listeners = new Map<string, Array<(event: unknown) => void>>()

  constructor(readonly url: URL | string) {
    FakeWebSocket.instances.push(this)
  }

  addEventListener(type: string, fn: (event: unknown) => void): void {
    const existing = this.#listeners.get(type) ?? []
    existing.push(fn)
    this.#listeners.set(type, existing)
  }

  send(): void {}

  close(): void {
    this.emit('close', {code: 1000})
  }

  emit(type: string, event: unknown = {}): void {
    for (const fn of this.#listeners.get(type) ?? []) {
      fn(event)
    }
  }

  static last(): FakeWebSocket {
    const socket = FakeWebSocket.instances[FakeWebSocket.instances.length - 1]
    if (socket === undefined) {
      throw new Error('No socket was opened')
    }
    return socket
  }
}

beforeEach(() => {
  vi.useFakeTimers()
  FakeWebSocket.instances = []
  vi.stubGlobal('WebSocket', FakeWebSocket)
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

const count = (): number => FakeWebSocket.instances.length

describe('reconnection', () => {
  it('retries after a close', () => {
    const socket = new ServerSocket()
    socket.connect()
    expect(count()).toBe(1)

    FakeWebSocket.last().emit('close', {code: 1006})
    vi.advanceTimersByTime(3000)

    expect(count()).toBe(2)
  })

  it('backs off rather than retrying at a fixed rate', () => {
    const socket = new ServerSocket()
    socket.connect()

    // Each failed attempt should wait longer than the one before it.
    FakeWebSocket.last().emit('close', {code: 1006})
    vi.advanceTimersByTime(3000)
    expect(count()).toBe(2)

    FakeWebSocket.last().emit('close', {code: 1006})
    vi.advanceTimersByTime(3000)
    expect(count()).toBe(2) // 3s is no longer enough
    vi.advanceTimersByTime(3000)
    expect(count()).toBe(3) // 6s is

    FakeWebSocket.last().emit('close', {code: 1006})
    vi.advanceTimersByTime(6000)
    expect(count()).toBe(3) // 6s is no longer enough either
    vi.advanceTimersByTime(6000)
    expect(count()).toBe(4)
  })

  it('resets the delay once a connection succeeds', () => {
    const socket = new ServerSocket()
    socket.connect()

    FakeWebSocket.last().emit('close', {code: 1006})
    vi.advanceTimersByTime(3000)
    FakeWebSocket.last().emit('close', {code: 1006})
    vi.advanceTimersByTime(6000)
    expect(count()).toBe(3)

    // A connection that came up is not part of the previous outage.
    FakeWebSocket.last().emit('open')
    FakeWebSocket.last().emit('close', {code: 1006})
    vi.advanceTimersByTime(3000)

    expect(count()).toBe(4)
  })

  it('stops retrying when the server rejects the session', () => {
    const socket = new ServerSocket()
    socket.connect()

    // 1008 is the policy violation the server sends an anonymous upgrade.
    // Retrying cannot produce a session, so it must not keep trying.
    FakeWebSocket.last().emit('close', {code: 1008})
    vi.advanceTimersByTime(600_000)

    expect(count()).toBe(1)
  })

  it('does not reconnect after close() was asked for', () => {
    const socket = new ServerSocket()
    socket.connect()
    socket.close()
    vi.advanceTimersByTime(60_000)

    expect(count()).toBe(1)
  })
})
