#!/usr/bin/env node
'use strict';

const lib = require('./lib');

async function handlePostToolUse() {
  return '';
}

if (require.main === module) {
  (async () => {
    await lib.RunHook('PostToolUse', handlePostToolUse);
  })();
}
