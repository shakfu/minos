import {describe, expect, it} from 'vitest'
import {formatTime, sortEntries} from '../src/apps/FileManager'
import type {FileEntry} from '../src/core/api'

const entry = (filename: string, isDirectory = false): FileEntry => ({
  isDirectory,
  isFile: !isDirectory,
  mime: isDirectory ? null : 'text/plain',
  size: 0,
  path: `home:/${filename}`,
  filename,
  stat: {
    size: 0,
    mode: 0,
    atime: '',
    mtime: '',
    ctime: '',
    atimeMs: 0,
    mtimeMs: 0,
    ctimeMs: 0
  }
})

describe('sortEntries', () => {
  it('puts directories first', () => {
    const sorted = sortEntries([entry('zeta'), entry('alpha', true)])
    expect(sorted.map(item => item.filename)).toEqual(['alpha', 'zeta'])
  })

  it('sorts each group by name, ignoring case', () => {
    const sorted = sortEntries([entry('beta'), entry('Alpha'), entry('gamma')])
    expect(sorted.map(item => item.filename)).toEqual(['Alpha', 'beta', 'gamma'])
  })

  it('does not mutate the input', () => {
    const input = [entry('b'), entry('a')]
    sortEntries(input)
    expect(input.map(item => item.filename)).toEqual(['b', 'a'])
  })
})

describe('formatTime', () => {
  it('renders an ISO timestamp', () => {
    expect(formatTime('2026-09-01T08:30:00+00:00')).not.toBe('')
  })

  it('renders nothing for an unusable value', () => {
    expect(formatTime('')).toBe('')
    expect(formatTime('not a date')).toBe('')
  })
})
