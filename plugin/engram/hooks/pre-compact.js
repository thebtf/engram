#!/usr/bin/env node
'use strict';

const lib = require('./lib');

async function handlePreCompact() {
  return '';
}

if (require.main === module) {
  (async () => {
    await lib.RunHook('PreCompact', handlePreCompact);
  })();
}

module.exports = {
  handlePreCompact,
};
