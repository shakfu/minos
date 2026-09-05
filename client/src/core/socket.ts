/**
 * The server's websocket.
 *
 * One connection carries every named message as a JSON {name, params} frame.
 * The upgrade needs a session, so this is only opened after login.
 */

import {Bus} from './bus'

export interface ServerMessage {
  name: string
  params: unknown[]
}

type SocketEvents = {
  open: null
  close: null
  message: ServerMessage
}

const RECONNECT_DELAY = 3000

/** Ceiling on the backoff, so a long outage still reconnects promptly. */
const RECONNECT_MAX = 60_000

/**
 * Policy violation: the server closes with this when the upgrade carries no
 * session. Retrying cannot fix that, so the socket stops instead of hammering.
 */
const POLICY_VIOLATION = 1008

export class ServerSocket {
  readonly bus = new Bus<SocketEvents>()

  #socket: WebSocket | null = null
  #retry: ReturnType<typeof setTimeout> | null = null
  #wanted = false
  #delay = RECONNECT_DELAY

  get connected(): boolean {
    return this.#socket?.readyState === WebSocket.OPEN
  }

  connect(): void {
    this.#wanted = true
    this.#delay = RECONNECT_DELAY
    this.#open()
  }

  close(): void {
    this.#wanted = false
    this.#clearRetry()
    this.#socket?.close()
    this.#socket = null
  }

  send(name: string, ...params: unknown[]): void {
    if (this.connected) {
      this.#socket?.send(JSON.stringify({name, params}))
    }
  }

  #open(): void {
    const url = new URL('/', window.location.href)
    url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:'

    const socket = new WebSocket(url)
    this.#socket = socket

    socket.addEventListener('open', () => {
      // A connection that lasted is not part of the previous outage.
      this.#delay = RECONNECT_DELAY
      this.bus.emit('open', null)
    })

    socket.addEventListener('message', event => {
      try {
        const frame = JSON.parse(String(event.data)) as ServerMessage
        this.bus.emit('message', {name: frame.name, params: frame.params ?? []})
      } catch {
        console.warn('Discarding malformed frame from server')
      }
    })

    socket.addEventListener('close', event => {
      this.#socket = null
      this.bus.emit('close', null)

      // The session is gone, and no amount of retrying will bring it back. The
      // page reloads into the login screen on its own once something touches
      // the API; until then, stop rather than reconnect every few seconds.
      if (event.code === POLICY_VIOLATION) {
        this.#wanted = false
        return
      }

      if (this.#wanted) {
        this.#retry = setTimeout(() => this.#open(), this.#delay)
        this.#delay = Math.min(this.#delay * 2, RECONNECT_MAX)
      }
    })
  }

  #clearRetry(): void {
    if (this.#retry !== null) {
      clearTimeout(this.#retry)
      this.#retry = null
    }
  }
}
