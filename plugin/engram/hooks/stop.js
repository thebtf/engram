#!/usr/bin/env node
'use strict';

async function handleStop() {
  return '';
}

if (require.main === module) {
  (async () => {
    process.stdout.write('');
  })();
}

module.exports = {
  handleStop,
};
