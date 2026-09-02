/**
 * Who is logged in, and what they have configured.
 *
 * The server has no endpoint that returns the current user, so a reload probes
 * GET /settings -- it answers 403 when anonymous -- and takes the profile from
 * local storage. A /me route would be the clean fix, but the API is frozen.
 */

import {api, ApiError, type Settings, type UserProfile} from './api'

const PROFILE_KEY = 'minos.profile'

/** The settings file is a flat object of namespaces, so we own one key. */
const NAMESPACE = 'minos/desktop'

/** The Flask session is rolling, so an idle desktop still needs to touch it. */
const KEEPALIVE_INTERVAL = 10 * 60 * 1000

export interface DesktopSettings {
  lastPath?: string
}

const readStoredProfile = (): UserProfile | null => {
  try {
    const raw = window.localStorage.getItem(PROFILE_KEY)
    return raw === null ? null : (JSON.parse(raw) as UserProfile)
  } catch {
    return null
  }
}

export class Session {
  #profile: UserProfile | null = null
  #settings: Settings = {}
  #keepalive: ReturnType<typeof setInterval> | null = null

  get profile(): UserProfile | null {
    return this.#profile
  }

  get username(): string {
    return this.#profile?.username ?? 'unknown'
  }

  /** True when a previous session is still valid on the server. */
  async restore(): Promise<boolean> {
    try {
      this.#settings = await api.loadSettings()
    } catch (error) {
      if (error instanceof ApiError && error.isUnauthenticated) {
        return false
      }
      throw error
    }

    this.#profile = readStoredProfile() ?? {
      id: 'unknown',
      username: 'unknown',
      name: 'unknown',
      groups: []
    }
    this.#startKeepalive()
    return true
  }

  async login(username: string, password: string): Promise<void> {
    this.#profile = await api.login(username, password)
    window.localStorage.setItem(PROFILE_KEY, JSON.stringify(this.#profile))
    this.#settings = await api.loadSettings()
    this.#startKeepalive()
  }

  async logout(): Promise<void> {
    this.#stopKeepalive()
    window.localStorage.removeItem(PROFILE_KEY)
    this.#profile = null
    this.#settings = {}
    await api.logout()
  }

  get desktop(): DesktopSettings {
    const stored = this.#settings[NAMESPACE]
    return typeof stored === 'object' && stored !== null ? (stored as DesktopSettings) : {}
  }

  /**
   * Merge into our namespace and write the whole file back. Other namespaces
   * live in the same object -- the retired OS.js client left `osjs/*` keys in
   * existing homes -- so a blind overwrite would drop them.
   */
  async patchDesktop(patch: DesktopSettings): Promise<void> {
    this.#settings = {...this.#settings, [NAMESPACE]: {...this.desktop, ...patch}}
    await api.saveSettings(this.#settings)
  }

  #startKeepalive(): void {
    this.#stopKeepalive()
    this.#keepalive = setInterval(() => {
      void api.ping().catch(() => undefined)
    }, KEEPALIVE_INTERVAL)
  }

  #stopKeepalive(): void {
    if (this.#keepalive !== null) {
      clearInterval(this.#keepalive)
      this.#keepalive = null
    }
  }
}
