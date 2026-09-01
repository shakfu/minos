/**
 * Modal prompts built on the platform's <dialog>, which brings the backdrop,
 * focus trapping and Escape-to-cancel with it.
 */

interface DialogOptions {
  message: string
  field?: {value: string; label: string}
  confirmLabel?: string
  cancelLabel?: string | null
}

const run = (options: DialogOptions): Promise<string | null> =>
  new Promise(resolve => {
    const dialog = document.createElement('dialog')
    dialog.className = 'dialog'

    const form = document.createElement('form')
    form.method = 'dialog'

    const message = document.createElement('p')
    message.className = 'dialog-message'
    message.textContent = options.message
    form.append(message)

    let input: HTMLInputElement | null = null
    if (options.field !== undefined) {
      const label = document.createElement('label')
      label.className = 'dialog-field'
      label.textContent = options.field.label

      input = document.createElement('input')
      input.type = 'text'
      input.value = options.field.value
      label.append(input)
      form.append(label)
    }

    const actions = document.createElement('div')
    actions.className = 'dialog-actions'

    if (options.cancelLabel !== null) {
      const cancel = document.createElement('button')
      cancel.type = 'button'
      cancel.className = 'btn'
      cancel.textContent = options.cancelLabel ?? 'Cancel'
      cancel.addEventListener('click', () => dialog.close(''))
      actions.append(cancel)
    }

    const confirm = document.createElement('button')
    confirm.type = 'submit'
    confirm.className = 'btn btn-primary'
    confirm.textContent = options.confirmLabel ?? 'OK'
    confirm.value = 'ok'
    actions.append(confirm)

    form.append(actions)
    dialog.append(form)
    document.body.append(dialog)

    // A dialog closed by Escape reports an empty returnValue, which is exactly
    // how the cancel button reports itself too.
    dialog.addEventListener('close', () => {
      const accepted = dialog.returnValue === 'ok'
      dialog.remove()
      resolve(accepted ? (input?.value ?? '') : null)
    })

    form.addEventListener('submit', () => {
      dialog.returnValue = 'ok'
    })

    dialog.showModal()
    input?.select()
  })

export const alert = (message: string): Promise<void> =>
  run({message, cancelLabel: null}).then(() => undefined)

export const confirm = (message: string, confirmLabel = 'OK'): Promise<boolean> =>
  run({message, confirmLabel}).then(result => result !== null)

export const prompt = (message: string, value = '', label = ''): Promise<string | null> =>
  run({message, field: {value, label}}).then(result =>
    result === null || result.trim() === '' ? null : result.trim()
  )
