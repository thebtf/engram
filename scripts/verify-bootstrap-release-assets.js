const fs = require("node:fs");
const { parsePolicy, validatePolicy } = require("../plugin/engram/scripts/bootstrap-policy.js");

function verifyReleaseAssets(rawPolicy, releases, tag) {
  if (typeof tag !== "string" || !tag.startsWith("v")) throw new Error("malformed bootstrap policy or release tag mismatch");
  let policy;
  try { policy = typeof rawPolicy === "string" ? parsePolicy(rawPolicy, tag.slice(1)) : validatePolicy(rawPolicy, tag.slice(1)); } catch { throw new Error("malformed bootstrap policy or release tag mismatch"); }
  const expected = Object.values(policy.targets).map(({ desired }) => desired);
  const matches = Array.isArray(releases) ? releases.filter((release) => release && release.tag_name === tag) : [];
  if (matches.length !== 1 || !Number.isSafeInteger(matches[0].id) || matches[0].draft !== true || !Array.isArray(matches[0].assets)) {
    throw new Error("expected exactly one private draft release");
  }
  for (const item of expected) {
    const assets = matches[0].assets.filter((asset) => asset && asset.name === item.asset);
    if (assets.length !== 1) throw new Error(`expected exactly one uploaded ${item.asset}`);
    const asset = assets[0];
    if (asset.state !== "uploaded" || asset.size !== item.size || asset.digest !== `sha256:${item.sha256}`) {
      throw new Error(`uploaded asset mismatch for ${item.asset}`);
    }
  }
  return matches[0].id;
}

function main(argv = process.argv.slice(2)) {
  const [policyPath, releasesPath, tag] = argv;
  if (!policyPath || !releasesPath || !tag) {
    throw new Error("usage: verify-bootstrap-release-assets.js POLICY RELEASES TAG");
  }
  const policy = fs.readFileSync(policyPath, "utf8");
  const releases = JSON.parse(fs.readFileSync(releasesPath, "utf8"));
  process.stdout.write(String(verifyReleaseAssets(policy, releases, tag)));
}

if (require.main === module) {
  try {
    main();
  } catch (error) {
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 1;
  }
}

module.exports = { main, verifyReleaseAssets };
