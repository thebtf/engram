#!/usr/bin/env node
'use strict';

function buildSearchRequest(project, prompt, cwd, filesBeingEdited) {
  const request = {
    project,
    query: prompt,
    cwd,
  };

  if (Array.isArray(filesBeingEdited) && filesBeingEdited.length > 0) {
    request.files_being_edited = filesBeingEdited;
  }

  return request;
}

async function handleUserPrompt() {
  return '';
}

if (require.main === module) {
  (async () => {
    process.stdout.write('');
  })();
}

module.exports = {
  buildSearchRequest,
  handleUserPrompt,
};
