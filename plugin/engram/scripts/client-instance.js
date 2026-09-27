"use strict";

const crypto = require("node:crypto");
const fs = require("node:fs");
const path = require("node:path");
const { assertSafeDirectory } = require("./ensure-binary.js");

const identityPattern = /^engram-[0-9a-f]{32}\n$/;

function installationClientInstanceID(pluginData) {
 const directory = assertSafeDirectory(pluginData);
 const destination = path.join(directory, "client-instance-id");
 function readIdentity() {
  let stat;
  try { stat = fs.lstatSync(destination); }
  catch (error) { if (error.code === "ENOENT") return ""; throw error; }
  if (!stat.isFile() || stat.isSymbolicLink() || stat.size !== 40) throw new Error("installed client identity is invalid");
  const value = fs.readFileSync(destination, "utf8");
  if (!identityPattern.test(value)) throw new Error("installed client identity is invalid");
  return value.trim();
 }
 const existing = readIdentity();
 if (existing) return existing;
 const staged = path.join(directory, `client-instance-id.${process.pid}.${crypto.randomBytes(16).toString("hex")}.tmp`);
 try {
  fs.writeFileSync(staged, `engram-${crypto.randomBytes(16).toString("hex")}\n`, { flag: "wx", mode: 0o600 });
  try { fs.linkSync(staged, destination); }
  catch (error) { if (error.code !== "EEXIST") throw error; }
  return readIdentity();
 } finally {
  try { fs.unlinkSync(staged); } catch (error) { if (error.code !== "ENOENT") throw error; }
 }
}

module.exports = { installationClientInstanceID };
