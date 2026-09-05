/**
 * The Streams dock, which is the one long-lived listener on the chat bus.
 *
 * A window is closed and reopened often, so anything the dock subscribes to has
 * to come back off the bus with it. Otherwise a closed dock keeps re-rendering
 * into a detached tree and every reopen adds another set of listeners.
 *
 * The observable is how often the dock renders, not what is in the document:
 * closing a window detaches its subtree, so a leaked dock and a released one
 * both leave nothing to query. Counting reads of the roster is what tells them
 * apart -- a render cannot happen without one.
 */

import {beforeEach, describe, expect, it, vi} from 'vitest'

import {openStreams} from '../src/apps/Chat'
import {Bus} from '../src/core/bus'
import type {ChatClient, Person, Room} from '../src/core/chat'
import type {Session} from '../src/core/session'
import {WindowManager} from '../src/wm/WindowManager'

const room = (id: string, title: string): Room => ({
  id,
  title,
  kind: 'chat',
  members: ['demo', 'alice'],
  lastSeq: 0
})

/** Enough of ChatClient for the dock, with every render counted. */
class FakeChat {
  readonly bus = new Bus<{
    message: never
    room: Room
    presence: Person
    synced: unknown
  }>()

  me = 'demo'
  rooms: Room[] = [room('r1', 'Design')]

  /** Bumped by #render, which cannot draw a roster without reading this. */
  renders = 0

  #users: Person[] = [
    {username: 'demo', online: true},
    {username: 'alice', online: false}
  ]

  get users(): Person[] {
    this.renders += 1
    return this.#users
  }

  sync(): Promise<unknown> {
    return Promise.resolve({me: this.me, users: this.#users, rooms: this.rooms})
  }

  room(id: string): Room | undefined {
    return this.rooms.find(r => r.id === id)
  }

  as(): ChatClient {
    return this as unknown as ChatClient
  }
}

let root: HTMLElement
let wm: WindowManager
let chat: FakeChat

const context = () => ({
  wm,
  session: {desktop: {}, patchDesktop: () => Promise.resolve()} as unknown as Session,
  chat: chat.as()
})

const items = (): Element[] => Array.from(document.querySelectorAll('.dock-item'))

/** Open a dock and wait for its first render to land. */
const openDock = async (): Promise<void> => {
  const before = chat.renders
  openStreams(context())
  await vi.waitFor(() => expect(chat.renders).toBeGreaterThan(before))
}

beforeEach(() => {
  document.body.replaceChildren()
  root = document.createElement('div')
  document.body.append(root)
  wm = new WindowManager(root, () => ({x: 0, y: 0, width: 1024, height: 768}))
  chat = new FakeChat()
})

describe('the dock', () => {
  it('renders the roster and the rooms after syncing', async () => {
    await openDock()

    const labels = items().map(item => item.textContent)
    // Everyone but you, and every room you are in.
    expect(labels).toContain('alice')
    expect(labels).not.toContain('demo')
    expect(labels.some(label => label?.startsWith('Design'))).toBe(true)
  })

  it('re-renders when a room arrives on the bus', async () => {
    await openDock()

    chat.rooms = [...chat.rooms, room('r2', 'Build')]
    chat.bus.emit('room', room('r2', 'Build'))

    expect(items().map(item => item.textContent).some(l => l?.startsWith('Build'))).toBe(true)
  })

  it('stops rendering once its window is closed', async () => {
    await openDock()

    wm.windows[0]?.close()
    const afterClose = chat.renders

    // A released dock ignores all three; a leaked one renders on each.
    chat.bus.emit('room', room('r2', 'Build'))
    chat.bus.emit('presence', {username: 'alice', online: true})
    chat.bus.emit('synced', {})

    expect(chat.renders).toBe(afterClose)
  })

  it('does not accumulate listeners across reopens', async () => {
    // Three docks opened and closed in turn, then a fourth left open. If close
    // did not release them, one event would render four times over.
    for (let index = 0; index < 3; index += 1) {
      await openDock()
      wm.windows[0]?.close()
    }
    await openDock()

    const before = chat.renders
    chat.bus.emit('presence', {username: 'alice', online: true})

    expect(chat.renders - before).toBe(1)
  })
})
