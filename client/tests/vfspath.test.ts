import {describe, expect, it} from 'vitest'
import * as vfs from '../src/core/vfspath'

describe('splitting a virtual path', () => {
  it('separates the mountpoint from the rest', () => {
    expect(vfs.split('home:/notes/todo.txt')).toEqual({mountpoint: 'home', rest: 'notes/todo.txt'})
  })

  it('treats a mountpoint root as an empty remainder', () => {
    expect(vfs.split('home:/')).toEqual({mountpoint: 'home', rest: ''})
  })

  it('rejects a path with no mountpoint', () => {
    expect(() => vfs.split('notes/todo.txt')).toThrow(/Malformed/)
  })
})

describe('navigating', () => {
  it('joins a name onto a directory', () => {
    expect(vfs.join('home:/', 'a.txt')).toBe('home:/a.txt')
    expect(vfs.join('home:/notes', 'a.txt')).toBe('home:/notes/a.txt')
  })

  it('walks up one level', () => {
    expect(vfs.parent('home:/notes/todo.txt')).toBe('home:/notes')
    expect(vfs.parent('home:/notes')).toBe('home:/')
  })

  it('stops at the mountpoint root', () => {
    expect(vfs.parent('home:/')).toBe('home:/')
    expect(vfs.isRoot('home:/')).toBe(true)
    expect(vfs.isRoot('home:/notes')).toBe(false)
  })

  it('reads the last segment', () => {
    expect(vfs.basename('home:/notes/todo.txt')).toBe('todo.txt')
    expect(vfs.basename('home:/')).toBe('')
  })
})

describe('the breadcrumb trail', () => {
  it('starts at the mountpoint label and accumulates segments', () => {
    expect(vfs.trail('home:/a/b')).toEqual([
      {label: 'Home', path: 'home:/'},
      {label: 'a', path: 'home:/a'},
      {label: 'b', path: 'home:/a/b'}
    ])
  })

  it('is a single crumb at a root', () => {
    expect(vfs.trail('osjs:/')).toEqual([{label: 'System', path: 'osjs:/'}])
  })
})

describe('mountpoint permissions', () => {
  it('mirrors the server: home writable, osjs not', () => {
    expect(vfs.isReadOnly('home:/a')).toBe(false)
    expect(vfs.isReadOnly('osjs:/a')).toBe(true)
  })

  it('refuses to treat an unknown mountpoint as writable', () => {
    expect(vfs.isReadOnly('nope:/a')).toBe(true)
  })
})

describe('formatSize', () => {
  it('keeps bytes whole and scales larger units', () => {
    expect(vfs.formatSize(0)).toBe('0 B')
    expect(vfs.formatSize(512)).toBe('512 B')
    expect(vfs.formatSize(1024)).toBe('1.0 KB')
    expect(vfs.formatSize(1536)).toBe('1.5 KB')
    expect(vfs.formatSize(1024 ** 3)).toBe('1.0 GB')
  })
})
