/**
 * A typed wrapper over the platform's EventTarget.
 *
 * Only enough to name events and their payloads; the dispatch itself is the
 * browser's.
 */

export class Bus<Events extends Record<string, unknown>> extends EventTarget {
  emit<K extends keyof Events & string>(type: K, detail: Events[K]): void {
    this.dispatchEvent(new CustomEvent(type, {detail}))
  }

  /** Subscribe, and get back the function that unsubscribes. */
  on<K extends keyof Events & string>(type: K, fn: (detail: Events[K]) => void): () => void {
    const listener = (event: Event) => fn((event as CustomEvent<Events[K]>).detail)
    this.addEventListener(type, listener)
    return () => this.removeEventListener(type, listener)
  }
}
