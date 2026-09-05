/**
 * The room window: the log, the unread count, and the drops that edit
 * membership.
 *
 * The subtle part is deduplication. ChatClient dedupes on a cursor shared by
 * every window on a room, while a freshly opened window renders its own history
 * from zero to fill an empty log. The two are independent, so a window has to
 * carry its own mark or a repair re-emitting what it already drew would draw it
 * twice.
 */

import {afterEach, beforeEach, describe, expect, it, vi} from 'vitest'

import {Bus} from '../src/core/bus'
import type {ChatClient, Message, Room} from '../src/core/chat'
import type {Session} from '../src/core/session'
import {WindowManager} from '../src/wm/WindowManager'

// The dialogs are the platform's <dialog>; stub them so a drop can be driven
// without depending on modal mechanics.
const confirmed = vi.fn(() => Promise.resolve(true))
const alerted = vi.fn(() => Promise.resolve())

vi.mock('../src/ui/Dialog', () => ({
  confirm: (...args: unknown[]) => confirmed(...(args as [])),
  alert: (...args: unknown[]) => alerted(...(args as [])),
  prompt: () => Promise.resolve(null)
}))

const {openRoom} = await import('../src/apps/Chat')

const USER_TYPE = 'application/x-minos-user'
const ROOM_TYPE = 'application/x-minos-room'

let nextRoom = 0

const makeRoom = (over: Partial<Room> = {}): Room => ({
  id: `r${++nextRoom}`,
  title: 'Design',
  kind: 'chat',
  members: ['demo', 'alice'],
  lastSeq: 0,
  ...over
})

const message = (seq: number, body: string, room: string): Message => ({
  room,
  seq,
  author: 'alice',
  kind: 'text',
  body,
  at: 0
})

class FakeChat {
  readonly bus = new Bus<{message: Message; room: Room; presence: never; synced: never}>()

  me = 'demo'
  history = vi.fn((_room: string) => Promise.resolve({messages: [] as Message[]}))
  send = vi.fn((_room: string, _body: string) => Promise.resolve({seq: 1}))
  invite = vi.fn((room: string, username: string) =>
    Promise.resolve(makeRoomWithId(room, ['demo', 'alice', username]))
  )
  merge = vi.fn((_source: string, into: string) =>
    Promise.resolve(makeRoomWithId(into, ['demo', 'alice', 'bob']))
  )
  leave = vi.fn((_room: string) => Promise.resolve())
  room = vi.fn((id: string) => makeRoomWithId(id, ['bob']))

  as(): ChatClient {
    return this as unknown as ChatClient
  }
}

const makeRoomWithId = (id: string, members: string[]): Room => ({
  id,
  title: 'Design',
  kind: 'chat',
  members,
  lastSeq: 0
})

let root: HTMLElement
let wm: WindowManager
let chat: FakeChat

const context = () => ({
  wm,
  session: {desktop: {}, patchDesktop: () => Promise.resolve()} as unknown as Session,
  chat: chat.as()
})

const lines = (): Element[] => Array.from(document.querySelectorAll('.room-line'))
const bodies = (): (string | null)[] =>
  Array.from(document.querySelectorAll('.room-body')).map(n => n.textContent)
const body = (): HTMLElement => {
  const el = document.querySelector<HTMLElement>('.room')
  if (el === null) {
    throw new Error('No room window is open')
  }
  return el
}

/** A drop carrying one of the two drag payloads. */
const drop = (target: HTMLElement, type: string, value: string): void => {
  const event = new Event('drop', {bubbles: true, cancelable: true})
  Object.defineProperty(event, 'dataTransfer', {
    value: {
      types: [type],
      getData: (asked: string) => (asked === type ? value : '')
    }
  })
  target.dispatchEvent(event)
}

/** Open a room and wait for its history to have been drawn. */
const open = async (room: Room): Promise<Room> => {
  openRoom(context(), room)
  await vi.waitFor(() => expect(chat.history).toHaveBeenCalledWith(room.id))
  return room
}

beforeEach(() => {
  document.body.replaceChildren()
  root = document.createElement('div')
  document.body.append(root)
  wm = new WindowManager(root, () => ({x: 0, y: 0, width: 1024, height: 768}))
  chat = new FakeChat()
  confirmed.mockClear()
  alerted.mockClear()
  confirmed.mockImplementation(() => Promise.resolve(true))
})

afterEach(() => {
  // RoomWindow keeps a module-level map of open rooms, released on close.
  // Leaving one behind would make the next open raise the stale window.
  for (const window_ of [...wm.windows]) {
    window_.close()
  }
})

describe('the log', () => {
  it('draws the history it loads', async () => {
    const room = makeRoom()
    chat.history.mockResolvedValue({
      messages: [message(1, 'one', room.id), message(2, 'two', room.id)]
    })
    await open(room)

    await vi.waitFor(() => expect(bodies()).toEqual(['one', 'two']))
  })

  it('appends a message that arrives on the bus', async () => {
    const room = await open(makeRoom())

    chat.bus.emit('message', message(1, 'live', room.id))

    expect(bodies()).toEqual(['live'])
  })

  it('ignores traffic for another room', async () => {
    const room = await open(makeRoom())

    chat.bus.emit('message', message(1, 'elsewhere', 'someone-else'))

    expect(lines()).toHaveLength(0)
    void room
  })

  it('does not draw a message twice when a repair re-emits it', async () => {
    // The window drew 1-3 from its own history call. ChatClient's cursor is
    // independent of that, so a repair can re-emit the same sequences.
    const room = makeRoom()
    chat.history.mockResolvedValue({
      messages: [
        message(1, 'one', room.id),
        message(2, 'two', room.id),
        message(3, 'three', room.id)
      ]
    })
    await open(room)
    await vi.waitFor(() => expect(lines()).toHaveLength(3))

    chat.bus.emit('message', message(2, 'two', room.id))
    chat.bus.emit('message', message(3, 'three', room.id))

    expect(bodies()).toEqual(['one', 'two', 'three'])
  })

  it('still draws what follows the messages it already has', async () => {
    const room = makeRoom()
    chat.history.mockResolvedValue({messages: [message(1, 'one', room.id)]})
    await open(room)
    await vi.waitFor(() => expect(lines()).toHaveLength(1))

    chat.bus.emit('message', message(1, 'one', room.id)) // seen
    chat.bus.emit('message', message(2, 'two', room.id)) // new

    expect(bodies()).toEqual(['one', 'two'])
  })

  it('reports a failed load in the log rather than throwing', async () => {
    chat.history.mockRejectedValue(new Error('Disconnected'))
    await open(makeRoom())

    await vi.waitFor(() => expect(lines()).toHaveLength(1))
    expect(lines()[0]?.textContent).toBe('Disconnected')
  })
})

describe('unread', () => {
  it('counts messages that arrive while the window is not focused', async () => {
    const room = await open(makeRoom())
    // Opening a second window takes focus off the room.
    wm.open({title: 'Other'})

    chat.bus.emit('message', message(1, 'a', room.id))
    chat.bus.emit('message', message(2, 'b', room.id))

    const titles = wm.windows.map(w => w.title)
    expect(titles.some(t => t.includes('(2)'))).toBe(true)
  })

  it('clears the count when the window is focused again', async () => {
    const room = await open(makeRoom())
    const other = wm.open({title: 'Other'})

    chat.bus.emit('message', message(1, 'a', room.id))
    expect(wm.windows.some(w => w.title.includes('(1)'))).toBe(true)

    const roomWindow = wm.windows.find(w => w !== other)
    roomWindow?.focus()

    expect(roomWindow?.title).toBe('Design')
  })

  it('does not count a message while the window has focus', async () => {
    const room = await open(makeRoom())

    chat.bus.emit('message', message(1, 'a', room.id))

    expect(wm.windows[0]?.title).toBe('Design')
  })
})

describe('membership by drop', () => {
  it('invites the person dropped on it', async () => {
    const room = await open(makeRoom())

    drop(body(), USER_TYPE, 'bob')
    await vi.waitFor(() => expect(chat.invite).toHaveBeenCalledWith(room.id, 'bob'))

    await vi.waitFor(() =>
      expect(document.querySelector('.room-names')?.textContent).toContain('bob')
    )
  })

  it('ignores a person who is already a member', async () => {
    await open(makeRoom())

    drop(body(), USER_TYPE, 'alice')

    expect(chat.invite).not.toHaveBeenCalled()
  })

  it('merges the room dropped on it, once confirmed', async () => {
    const room = await open(makeRoom())

    drop(body(), ROOM_TYPE, 'other-room')
    await vi.waitFor(() => expect(chat.merge).toHaveBeenCalledWith('other-room', room.id))
  })

  it('does not merge when the confirmation is declined', async () => {
    confirmed.mockImplementation(() => Promise.resolve(false))
    await open(makeRoom())

    drop(body(), ROOM_TYPE, 'other-room')
    await vi.waitFor(() => expect(confirmed).toHaveBeenCalled())

    expect(chat.merge).not.toHaveBeenCalled()
  })

  it('refuses to merge a room into itself', async () => {
    const room = await open(makeRoom())

    drop(body(), ROOM_TYPE, room.id)

    expect(chat.merge).not.toHaveBeenCalled()
  })
})

describe('a stream', () => {
  it('has no composer and no way to leave', async () => {
    await open(makeRoom({kind: 'stream', title: 'System'}))

    expect(document.querySelector('.room-composer')).toBeNull()
    expect(document.querySelector('.room-leave')).toBeNull()
  })

  it('still shows its log', async () => {
    const room = makeRoom({kind: 'stream'})
    chat.history.mockResolvedValue({messages: [message(1, 'demo wrote home:/x', room.id)]})
    await open(room)

    await vi.waitFor(() => expect(bodies()).toEqual(['demo wrote home:/x']))
  })
})

describe('lifecycle', () => {
  it('stops rendering once closed', async () => {
    const room = await open(makeRoom())
    chat.bus.emit('message', message(1, 'before', room.id))
    expect(lines()).toHaveLength(1)

    // Closing detaches the window's subtree, so the log is out of the document
    // whether or not the listener was released -- querying it proves nothing.
    // The window object outlives the close, and a leaked listener still drives
    // it: an unread message would retitle it. That is the observable.
    const roomWindow = wm.windows[0]
    roomWindow?.close()

    chat.bus.emit('message', message(2, 'after', room.id))

    expect(roomWindow?.title).toBe('Design')
  })

  it('raises the existing window rather than opening a second', async () => {
    const room = await open(makeRoom())

    openRoom(context(), room)

    expect(wm.windows).toHaveLength(1)
  })

  it('can be reopened after it is closed', async () => {
    const room = await open(makeRoom())
    wm.windows[0]?.close()
    expect(wm.windows).toHaveLength(0)

    await open(room)

    expect(wm.windows).toHaveLength(1)
  })
})
