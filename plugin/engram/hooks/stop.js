#!/usr/bin/env node
'use strict';

const lib = require('./lib');

async function handleStop() {
  return '';
}

if (require.main === module) {
  (async () => {
    await lib.RunHook('Stop', handleStop);
  })();
}

module.exports = {
  handleStop,
};
