/**
 * Boot: restore or ask for a session, then build the desktop.
 */

import './style.css'
import {APPS, type AppContext} from './apps'
import {ChatClient} from './core/chat'
import {Session} from './core/session'
import {ServerSocket, type ServerMessage} from './core/socket'
import {showLogin} from './ui/Login'
import {SEPARATOR, showMenu} from './ui/Menu'
import {Panel} from './ui/Panel'
import {WindowManager} from './wm/WindowManager'

const logout = (session: Session): void => {
  void session
    .logout()
    .catch(() => undefined)
    .then(() => window.location.reload())
}

const HANDLED = new Set([
  // Keepalive traffic; the panel already reflects the connection through the
  // socket's own open and close.
  'osjs/core:connected',
  'osjs/core:ping',
  // Claimed by ChatClient, which subscribes to the socket itself.
  'osjs/application:socket:message'
])

const onServerMessage = (message: ServerMessage): void => {
  if (!HANDLED.has(message.name)) {
    console.debug('Unhandled server message', message.name, message.params)
  }
}

const startDesktop = (root: HTMLElement, session: Session): void => {
  const desktop = document.createElement('main')
  desktop.className = 'desktop'

  // Windows are absolutely positioned inside the desktop, so its own box is
  // the coordinate space and the origin is always zero.
  const manager = new WindowManager(desktop, () => ({
    x: 0,
    y: 0,
    width: desktop.clientWidth,
    height: desktop.clientHeight
  }))

  // The socket comes first: the chat client subscribes to it, and the app
  // context carries that client to every window that needs a conversation.
  const socket = new ServerSocket()
  const chat = new ChatClient(socket)

  const context: AppContext = {wm: manager, session, chat}
  const panel = new Panel(context, () => logout(session))
  root.replaceChildren(panel.el, desktop)

  socket.bus.on('open', () => panel.setConnected(true))
  socket.bus.on('close', () => panel.setConnected(false))
  socket.bus.on('message', onServerMessage)
  socket.connect()

  desktop.addEventListener('contextmenu', event => {
    if (event.target !== desktop) {
      return
    }
    event.preventDefault()
    showMenu(
      [
        ...APPS.map(app => ({label: app.title, onSelect: () => app.open(context)})),
        SEPARATOR,
        {label: 'Log out', onSelect: () => logout(session)}
      ],
      {x: event.clientX, y: event.clientY}
    )
  })

  APPS[0]?.open(context)
}

const boot = async (): Promise<void> => {
  const root = document.createElement('div')
  root.className = 'root'
  document.body.append(root)

  const session = new Session()
  const restored = await session.restore().catch(() => false)

  if (!restored) {
    await showLogin(session, root)
  }

  startDesktop(root, session)
}

void boot()
