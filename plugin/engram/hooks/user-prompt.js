#!/usr/bin/env node
'use strict';

const lib = require('./lib');

async function handleUserPrompt() {
  return '';
}

if (require.main === module) {
  (async () => {
    await lib.RunHook('UserPromptSubmit', handleUserPrompt);
  })();
}

module.exports = {
  handleUserPrompt,
};
