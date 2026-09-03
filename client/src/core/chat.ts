/**
 * The client half of the Chat protocol.
 *
 * Everything rides `osjs/application:socket:message`, which is the only inbound
 * name the server accepts and the one extension point the frozen OS.js HTTP
 * contract leaves open. A request carries a `pid` the server quotes back, so
 * several in flight at once can be told apart; a frame whose `pid` is null is
 * not an answer to anything but a push from the bus.
 *
 * The bus does not guarantee delivery -- it is ZeroMQ PUB/SUB, which drops
 * rather than queues -- so this module treats every arriving message as
 * possibly out of order, duplicated or missing. The per-room sequence number is
 * what makes that recoverable: a gap between the cursor and what just arrived
 * is repaired by asking for the difference. That single mechanism covers a
 * dropped frame, a subscription that had not propagated yet, and a reconnect
 * after an hour offline.
 */

import {Bus} from './bus'
import type {ServerSocket} from './socket'

const APPLICATION_MESSAGE = 'osjs/application:socket:message'
const APPLICATION = 'Chat'

export type RoomKind = 'chat' | 'stream'
export type MessageKind = 'text' | 'event'

export interface Room {
  id: string
  title: string
  kind: RoomKind
  members: string[]
  lastSeq: number
}

export interface Message {
  room: string
  seq: number
  author: string
  kind: MessageKind
  body: string
  at: number
}

export interface Person {
  username: string
  online: boolean
}

interface SyncReply {
  me: string
  users: Person[]
  rooms: Room[]
}

type ChatEvents = {
  message: Message
  room: Room
  presence: Person
  synced: SyncReply
}

interface Envelope {
  pid: number | null
  args?: unknown[]
}

/** What the server pushes when `pid` is null. */
type PushEvent =
  | ({type: 'message'} & Message)
  | {type: 'room'; room: Room}
  | ({type: 'presence'} & Person)

export class ChatError extends Error {}

export class ChatClient {
  readonly bus = new Bus<ChatEvents>()

  #socket: ServerSocket
  #pid = 0
  #pending = new Map<number, {resolve: (value: never) => void; reject: (error: Error) => void}>()

  /** Highest sequence applied per room: the cursor a gap is measured against. */
  #cursors = new Map<string, number>()

  /** Rooms with a backfill in flight, so a burst cannot start several. */
  #repairing = new Set<string>()

  #me = ''
  #users: Person[] = []
  #rooms = new Map<string, Room>()

  constructor(socket: ServerSocket) {
    this.#socket = socket
    socket.bus.on('message', frame => {
      if (frame.name === APPLICATION_MESSAGE) {
        this.#receive(frame.params[0] as Envelope)
      }
    })

    // A reconnect is just a very long gap. Re-syncing re-subscribes the worker
    // and hands back every room's current lastSeq, which the cursors then
    // reconcile against.
    socket.bus.on('open', () => void this.sync().catch(() => undefined))
    socket.bus.on('close', () => this.#failPending(new ChatError('Disconnected')))
  }

  get me(): string {
    return this.#me
  }

  get users(): readonly Person[] {
    return this.#users
  }

  get rooms(): readonly Room[] {
    return [...this.#rooms.values()]
  }

  room(id: string): Room | undefined {
    return this.#rooms.get(id)
  }

  // -- operations -----------------------------------------------------------

  async sync(): Promise<SyncReply> {
    const reply = await this.#request<SyncReply>({op: 'sync'})
    this.#me = reply.me
    this.#users = reply.users
    for (const room of reply.rooms) {
      this.#rooms.set(room.id, room)
    }

    // Backfill before announcing: a listener that renders on `synced` should
    // not have to render again immediately for what it missed.
    await Promise.all(reply.rooms.map(room => this.#catchUp(room)))
    this.bus.emit('synced', reply)
    return reply
  }

  history(room: string, since = 0): Promise<{messages: Message[]}> {
    return this.#request<{messages: Message[]}>({op: 'history', room, since})
  }

  send(room: string, body: string): Promise<{seq: number}> {
    return this.#request<{seq: number}>({op: 'send', room, body})
  }

  async open(members: string[], title?: string): Promise<Room> {
    return this.#track(await this.#request<Room>({op: 'open', members, title}))
  }

  async invite(room: string, username: string): Promise<Room> {
    const reply = await this.#request<{room: Room}>({op: 'invite', room, username})
    return this.#track(reply.room)
  }

  async merge(room: string, into: string): Promise<Room> {
    const reply = await this.#request<{room: Room}>({op: 'merge', room, into})
    return this.#track(reply.room)
  }

  async leave(room: string): Promise<void> {
    await this.#request({op: 'leave', room})
    this.#rooms.delete(room)
    this.#cursors.delete(room)
  }

  /** The cursor a window should render from, and repair against. */
  cursor(room: string): number {
    return this.#cursors.get(room) ?? 0
  }

  // -- delivery -------------------------------------------------------------

  #receive(envelope: Envelope | undefined): void {
    if (envelope === undefined) {
      return
    }

    if (envelope.pid === null) {
      this.#dispatch(envelope.args?.[0])
      return
    }

    const pending = this.#pending.get(envelope.pid)
    if (pending === undefined) {
      return
    }
    this.#pending.delete(envelope.pid)

    const reply = envelope.args?.[0] as {error?: string} | undefined
    if (reply !== undefined && typeof reply.error === 'string') {
      pending.reject(new ChatError(reply.error))
    } else {
      pending.resolve(reply as never)
    }
  }

  #dispatch(event: unknown): void {
    if (typeof event !== 'object' || event === null || !('type' in event)) {
      return
    }

    const push = event as PushEvent
    if (push.type === 'message') {
      this.#apply(push)
    } else if (push.type === 'room') {
      this.#track(push.room)
      this.bus.emit('room', push.room)
    } else if (push.type === 'presence') {
      this.#users = this.#users.map(user =>
        user.username === push.username ? {...user, online: push.online} : user
      )
      this.bus.emit('presence', {username: push.username, online: push.online})
    }
  }

  /**
   * Apply one message against the room's cursor.
   *
   * Three cases, and the middle one is the whole point: a sequence at or below
   * the cursor has been seen already, one exactly above it is the next
   * message, and anything higher means something never arrived.
   */
  #apply(message: Message): void {
    const cursor = this.cursor(message.room)

    if (message.seq <= cursor) {
      return
    }

    if (message.seq > cursor + 1) {
      // Dropping this copy is safe: the server stored it before publishing, so
      // the backfill about to be requested is guaranteed to contain it.
      void this.#repair(message.room)
      return
    }

    this.#cursors.set(message.room, message.seq)
    this.bus.emit('message', message)
  }

  async #repair(room: string): Promise<void> {
    if (this.#repairing.has(room)) {
      return
    }
    this.#repairing.add(room)
    try {
      const {messages} = await this.history(room, this.cursor(room))
      for (const message of messages) {
        if (message.seq > this.cursor(room)) {
          this.#cursors.set(message.room, message.seq)
          this.bus.emit('message', message)
        }
      }
    } catch {
      // A repair that fails leaves the cursor where it was, so the next
      // message through re-detects the same gap and tries again.
    } finally {
      this.#repairing.delete(room)
    }
  }

  /** Fetch what a room holds beyond the cursor, on sync and on reconnect. */
  async #catchUp(room: Room): Promise<void> {
    if (room.lastSeq > this.cursor(room.id)) {
      await this.#repair(room.id)
    }
  }

  #track(room: Room): Room {
    this.#rooms.set(room.id, room)
    return room
  }

  // -- transport ------------------------------------------------------------

  #request<T>(body: Record<string, unknown>): Promise<T> {
    const pid = ++this.#pid
    return new Promise<T>((resolve, reject) => {
      if (!this.#socket.connected) {
        reject(new ChatError('Disconnected'))
        return
      }
      this.#pending.set(pid, {
        resolve: resolve as (value: never) => void,
        reject
      })
      this.#socket.send(APPLICATION_MESSAGE, {pid, name: APPLICATION, args: [body]})
    })
  }

  #failPending(error: Error): void {
    for (const pending of this.#pending.values()) {
      pending.reject(error)
    }
    this.#pending.clear()
  }
}
