#!/bin/bash
# Workflow prepare script for CI
# Called by shared GitHub Actions workflow before build/test steps

set -e

# Detect host OS for platform-specific setup (Windows needs SQLite headers for CGO tests)
HOST_OS=$(uname -s | tr '[:upper:]' '[:lower:]')

# On Windows, install SQLite development headers for CGO (used by test build tags)
if [[ "$HOST_OS" == mingw* ]] || [[ "$HOST_OS" == msys* ]] || [[ "$HOST_OS" == cygwin* ]]; then
    echo "Installing SQLite for Windows..."

    # Download SQLite amalgamation and set up for CGO
    SQLITE_VERSION="3470200"
    SQLITE_YEAR="2024"
    SQLITE_DIR="/c/sqlite"
    SQLITE_URL="https://www.sqlite.org/${SQLITE_YEAR}/sqlite-amalgamation-${SQLITE_VERSION}.zip"

    mkdir -p "$SQLITE_DIR"
    curl -sSL "$SQLITE_URL" -o /tmp/sqlite.zip
    unzip -q /tmp/sqlite.zip -d /tmp/
    cp /tmp/sqlite-amalgamation-${SQLITE_VERSION}/* "$SQLITE_DIR/"
    rm -rf /tmp/sqlite.zip /tmp/sqlite-amalgamation-${SQLITE_VERSION}

    echo "SQLite setup complete"
fi
