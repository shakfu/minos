/**
 * Read-only preview for the file manager. Text and images render inline;
 * anything else is handed to the browser as a download.
 */

import {api, type FileEntry} from '../core/api'
import type {AppContext} from './index'

const TEXT_MIMES = /^(text\/|application\/(json|javascript|xml|x-python|x-sh))/

export const canPreview = (entry: FileEntry): boolean =>
  entry.mime !== null && (TEXT_MIMES.test(entry.mime) || entry.mime.startsWith('image/'))

export const download = (entry: FileEntry): void => {
  window.open(api.fileUrl(entry.path, true), '_blank')
}

export const openViewer = (context: AppContext, entry: FileEntry): void => {
  if (!canPreview(entry)) {
    download(entry)
    return
  }

  const window_ = context.wm.open({
    title: entry.filename,
    width: 560,
    height: 420,
    minWidth: 280,
    minHeight: 200
  })
  window_.body.classList.add('viewer')

  if (entry.mime?.startsWith('image/') === true) {
    const image = document.createElement('img')
    image.className = 'viewer-image'
    image.alt = entry.filename
    image.src = api.fileUrl(entry.path)
    window_.body.append(image)
    return
  }

  const pre = document.createElement('pre')
  pre.className = 'viewer-text'
  pre.textContent = 'Loading...'
  window_.body.append(pre)

  api
    .readtext(entry.path)
    .then(text => {
      pre.textContent = text
    })
    .catch((cause: unknown) => {
      pre.textContent = `Could not read this file.\n\n${String(cause)}`
    })
}
