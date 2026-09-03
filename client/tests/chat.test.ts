/**
 * The delivery contract, which is the only part of chat that is subtle.
 *
 * The bus drops rather than queues, so these tests are mostly about what the
 * client does with a sequence number that is not the one it expected.
 */

import {beforeEach, describe, expect, it, vi} from 'vitest'

import {Bus} from '../src/core/bus'
import {ChatClient, type Message} from '../src/core/chat'
import type {ServerSocket} from '../src/core/socket'

const APPLICATION_MESSAGE = 'osjs/application:socket:message'

interface Request {
  pid: number
  name: string
  args: [Record<string, unknown>]
}

/** A socket that records what was sent and lets a test play the server. */
class FakeSocket {
  readonly bus = new Bus<{open: null; close: null; message: {name: string; params: unknown[]}}>()
  readonly sent: Request[] = []

  connected = true

  send(name: string, ...params: unknown[]): void {
    expect(name).toBe(APPLICATION_MESSAGE)
    this.sent.push(params[0] as Request)
  }

  at(index: number): Request {
    const request = this.sent[index]
    if (request === undefined) {
      throw new Error(`No request at ${index}`)
    }
    return request
  }

  /** The last request for an operation, so a test can quote its pid back. */
  lastRequest(op: string): Request {
    const match = [...this.sent].reverse().find(request => request.args[0].op === op)
    if (match === undefined) {
      throw new Error(`No ${op} request was sent`)
    }
    return match
  }

  reply(pid: number | null, payload: unknown): void {
    this.bus.emit('message', {name: APPLICATION_MESSAGE, params: [{pid, args: [payload]}]})
  }

  push(event: unknown): void {
    this.reply(null, event)
  }

  as(): ServerSocket {
    return this as unknown as ServerSocket
  }
}

const message = (seq: number, body: string, room = 'r1'): Message & {type: 'message'} => ({
  type: 'message',
  room,
  seq,
  author: 'alice',
  kind: 'text',
  body,
  at: 0
})

describe('requests', () => {
  let socket: FakeSocket
  let chat: ChatClient

  beforeEach(() => {
    socket = new FakeSocket()
    chat = new ChatClient(socket.as())
  })

  it('quotes a pid so concurrent requests can be told apart', async () => {
    const first = chat.history('r1')
    const second = chat.history('r2')

    const a = socket.at(0)
    const b = socket.at(1)
    expect(a.pid).not.toBe(b.pid)
    expect(a.name).toBe('Chat')

    socket.reply(b.pid, {messages: [message(1, 'second')]})
    socket.reply(a.pid, {messages: [message(1, 'first')]})

    expect((await first).messages[0]?.body).toBe('first')
    expect((await second).messages[0]?.body).toBe('second')
  })

  it('rejects when the server answers with an error', async () => {
    const pending = chat.send('r1', 'hello')
    socket.reply(socket.lastRequest('send').pid, {error: 'Not a member of that room'})

    await expect(pending).rejects.toThrow('Not a member of that room')
  })

  it('rejects everything in flight when the socket drops', async () => {
    const pending = chat.history('r1')
    socket.bus.emit('close', null)

    await expect(pending).rejects.toThrow('Disconnected')
  })
})

describe('delivery', () => {
  let socket: FakeSocket
  let chat: ChatClient
  let seen: Message[]

  beforeEach(() => {
    socket = new FakeSocket()
    chat = new ChatClient(socket.as())
    seen = []
    chat.bus.on('message', received => seen.push(received))
  })

  it('emits messages that follow the cursor', () => {
    socket.push(message(1, 'one'))
    socket.push(message(2, 'two'))

    expect(seen.map(m => m.body)).toEqual(['one', 'two'])
    expect(chat.cursor('r1')).toBe(2)
  })

  it('ignores a message it has already applied', () => {
    socket.push(message(1, 'one'))
    socket.push(message(1, 'one again'))

    expect(seen.map(m => m.body)).toEqual(['one'])
  })

  it('fills a gap from history rather than showing the wrong order', async () => {
    socket.push(message(1, 'one'))
    socket.push(message(4, 'four'))

    // The gap is repaired by asking for everything past the cursor, and the
    // dropped copy of message four comes back in that reply.
    const request = socket.lastRequest('history')
    expect(request.args[0]).toMatchObject({room: 'r1', since: 1})

    socket.reply(request.pid, {
      messages: [message(2, 'two'), message(3, 'three'), message(4, 'four')]
    })

    await vi.waitFor(() => expect(seen).toHaveLength(4))
    expect(seen.map(m => m.body)).toEqual(['one', 'two', 'three', 'four'])
    expect(chat.cursor('r1')).toBe(4)
  })

  it('starts one repair for a burst, not one per message', async () => {
    socket.push(message(5, 'five'))
    socket.push(message(6, 'six'))
    socket.push(message(7, 'seven'))

    expect(socket.sent.filter(request => request.args[0].op === 'history')).toHaveLength(1)

    socket.reply(socket.lastRequest('history').pid, {
      messages: [message(1, 'one'), message(2, 'two')]
    })
    await vi.waitFor(() => expect(chat.cursor('r1')).toBe(2))
  })

  it('keeps rooms on separate cursors', () => {
    socket.push(message(1, 'a', 'r1'))
    socket.push(message(1, 'b', 'r2'))

    expect(seen.map(m => m.body)).toEqual(['a', 'b'])
    expect(chat.cursor('r2')).toBe(1)
  })

  it('tracks presence without a re-sync', async () => {
    const pending = chat.sync()
    socket.reply(socket.lastRequest('sync').pid, {
      me: 'demo',
      users: [
        {username: 'demo', online: true},
        {username: 'alice', online: false}
      ],
      rooms: []
    })
    await pending

    socket.push({type: 'presence', username: 'alice', online: true})
    expect(chat.users.find(user => user.username === 'alice')?.online).toBe(true)
  })

  it('learns about a room it was added to while it was not watching', async () => {
    const pending = chat.sync()
    socket.reply(socket.lastRequest('sync').pid, {me: 'demo', users: [], rooms: []})
    await pending

    socket.push({
      type: 'room',
      room: {id: 'r9', title: 'Design', kind: 'chat', members: ['demo', 'alice'], lastSeq: 0}
    })

    expect(chat.room('r9')?.title).toBe('Design')
  })
})
