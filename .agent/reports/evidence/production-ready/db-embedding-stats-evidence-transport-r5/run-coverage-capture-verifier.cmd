@echo off
set "NODE_V8_COVERAGE="
set "NODE_TEST_CONTEXT="
node.exe "%~dp0verify-coverage-capture.cjs" %*
