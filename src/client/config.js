/*
 * Client configuration.
 *
 * Complete tree: https://github.com/os-js/osjs-client/blob/master/src/config.js
 */

const origin = window.location.origin

export default {
  // Served from /osjs.html, but the API and the package files live at the
  // root. Both of these default to the page's own location, which would send
  // requests to /osjs.html/vfs/... instead.
  http: {
    public: '/',
    uri: `${origin}/`
  },

  ws: {
    uri: `${origin.replace(/^http/, 'ws')}/`
  },

  auth: {
    login: {
      username: 'demo',
      password: 'demo'
    }
  },

  desktop: {
    settings: {
      // A flat fill rather than the stock wallpaper. 'color' skips the image
      // path entirely, so background.src is left unread. Users can still pick
      // an image through the desktop context menu; that writes to their own
      // settings and overrides this default.
      background: {
        color: '#ffffff',
        style: 'color'
      }
    }
  }
};
