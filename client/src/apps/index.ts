import type {ChatClient} from '../core/chat'
import type {Session} from '../core/session'
import type {WindowManager} from '../wm/WindowManager'
import {openStreams} from './Chat'
import {openFileManager} from './FileManager'

export interface AppContext {
  wm: WindowManager
  session: Session
  chat: ChatClient
}

export interface AppDefinition {
  id: string
  title: string
  open: (context: AppContext) => void
}

export const APPS: readonly AppDefinition[] = [
  {id: 'files', title: 'Files', open: openFileManager},
  {id: 'streams', title: 'Streams', open: openStreams}
]
