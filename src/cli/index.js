/*
 * osjs-cli configuration.
 *
 * Without this, package discovery only scans node_modules, so a theme or
 * application written locally under src/packages/ is never placed into dist/.
 *
 * https://manual.os-js.org/guide/cli/
 */

const path = require('path');

module.exports = {
  discover: [
    path.resolve(__dirname, '../packages')
  ],
  tasks: []
};
