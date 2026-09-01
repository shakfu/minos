/**
 * Virtual path helpers.
 *
 * The server addresses files as "<mountpoint>:/<path>". These are the only
 * functions that take that format apart, so the rest of the client can treat a
 * path as an opaque string.
 */

export interface Mountpoint {
  readonly name: string
  readonly label: string
  readonly readOnly: boolean
}

/**
 * Mirrors MOUNTPOINTS in server/vfs.py. The server has no endpoint that lists
 * them, so the client carries its own copy.
 */
export const MOUNTPOINTS: readonly Mountpoint[] = [
  {name: 'home', label: 'Home', readOnly: false},
  {name: 'osjs', label: 'System', readOnly: true}
]

export const root = (mountpoint: string): string => `${mountpoint}:/`

export const split = (path: string): {mountpoint: string; rest: string} => {
  const index = path.indexOf(':')
  if (index === -1) {
    throw new Error(`Malformed VFS path: ${path}`)
  }
  return {
    mountpoint: path.slice(0, index),
    rest: path.slice(index + 1).replace(/^\/+/, '')
  }
}

export const mountpointOf = (path: string): Mountpoint | undefined =>
  MOUNTPOINTS.find(m => m.name === split(path).mountpoint)

export const isReadOnly = (path: string): boolean => mountpointOf(path)?.readOnly ?? true

export const basename = (path: string): string => {
  const {rest} = split(path)
  const segments = rest.split('/').filter(Boolean)
  return segments[segments.length - 1] ?? ''
}

export const join = (path: string, name: string): string => {
  const {mountpoint, rest} = split(path)
  const segments = [...rest.split('/'), name].filter(Boolean)
  return `${mountpoint}:/${segments.join('/')}`
}

/** The parent directory, or the same path when already at a mountpoint root. */
export const parent = (path: string): string => {
  const {mountpoint, rest} = split(path)
  const segments = rest.split('/').filter(Boolean)
  segments.pop()
  return `${mountpoint}:/${segments.join('/')}`
}

export const isRoot = (path: string): boolean => split(path).rest === ''

/** Path segments as {label, path} pairs, for a breadcrumb trail. */
export const trail = (path: string): {label: string; path: string}[] => {
  const {mountpoint, rest} = split(path)
  const label = mountpointOf(path)?.label ?? mountpoint
  const crumbs = [{label, path: root(mountpoint)}]

  let walked = root(mountpoint)
  for (const segment of rest.split('/').filter(Boolean)) {
    walked = join(walked, segment)
    crumbs.push({label: segment, path: walked})
  }
  return crumbs
}

const UNITS = ['B', 'KB', 'MB', 'GB', 'TB']

export const formatSize = (bytes: number): string => {
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < UNITS.length - 1) {
    value /= 1024
    unit += 1
  }
  return unit === 0 ? `${value} ${UNITS[0]}` : `${value.toFixed(1)} ${UNITS[unit]}`
}
