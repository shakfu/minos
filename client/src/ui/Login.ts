/**
 * The login screen. Resolves once the server has accepted a session.
 */

import {ApiError} from '../core/api'
import type {Session} from '../core/session'

export const showLogin = (session: Session, mount: HTMLElement): Promise<void> =>
  new Promise(resolve => {
    const screen = document.createElement('div')
    screen.className = 'login'

    const form = document.createElement('form')
    form.className = 'login-form'

    const heading = document.createElement('h1')
    heading.className = 'login-title'
    heading.textContent = 'minos'

    const error = document.createElement('p')
    error.className = 'login-error'
    error.hidden = true
    error.setAttribute('role', 'alert')

    const username = field('Username', 'text', 'demo', 'username')
    const password = field('Password', 'password', 'demo', 'current-password')

    const submit = document.createElement('button')
    submit.type = 'submit'
    submit.className = 'btn btn-primary'
    submit.textContent = 'Sign in'

    form.append(heading, error, username.label, password.label, submit)
    screen.append(form)
    mount.append(screen)
    username.input.focus()

    form.addEventListener('submit', event => {
      event.preventDefault()
      error.hidden = true
      submit.disabled = true

      session
        .login(username.input.value, password.input.value)
        .then(() => {
          screen.remove()
          resolve()
        })
        .catch((cause: unknown) => {
          error.textContent =
            cause instanceof ApiError ? cause.message : 'Could not reach the server'
          error.hidden = false
          submit.disabled = false
          password.input.select()
        })
    })
  })

const field = (
  text: string,
  type: string,
  value: string,
  autocomplete: string
): {label: HTMLLabelElement; input: HTMLInputElement} => {
  const label = document.createElement('label')
  label.className = 'login-field'
  label.append(text)

  const input = document.createElement('input')
  input.type = type
  input.value = value
  input.required = true
  input.setAttribute('autocomplete', autocomplete)
  label.append(input)

  return {label, input}
}
