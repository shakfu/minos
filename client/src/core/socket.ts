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

export class ServerSocket {
  readonly bus = new Bus<SocketEvents>()

  #socket: WebSocket | null = null
  #retry: ReturnType<typeof setTimeout> | null = null
  #wanted = false

  get connected(): boolean {
    return this.#socket?.readyState === WebSocket.OPEN
  }

  connect(): void {
    this.#wanted = true
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

    socket.addEventListener('open', () => this.bus.emit('open', null))

    socket.addEventListener('message', event => {
      try {
        const frame = JSON.parse(String(event.data)) as ServerMessage
        this.bus.emit('message', {name: frame.name, params: frame.params ?? []})
      } catch {
        console.warn('Discarding malformed frame from server')
      }
    })

    socket.addEventListener('close', () => {
      this.#socket = null
      this.bus.emit('close', null)
      if (this.#wanted) {
        this.#retry = setTimeout(() => this.#open(), RECONNECT_DELAY)
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
