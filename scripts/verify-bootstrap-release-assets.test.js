const assert = require("node:assert/strict");
const test = require("node:test");
const policy = require("../plugin/engram/bootstrap-targets.json");
const { verifyReleaseAssets } = require("./verify-bootstrap-release-assets.js");

function draft() {
  return {
    id: 47,
    tag_name: "v6.47.0",
    draft: true,
    assets: Object.values(policy.targets).map(({ desired }) => ({
      name: desired.asset,
      state: "uploaded",
      size: desired.size,
      digest: `sha256:${desired.sha256}`,
    })),
  };
}

test("accepts one exact private draft and returns its release id", () => {
  assert.equal(verifyReleaseAssets(policy, [draft()], "v6.47.0"), 47);
});

test("rejects a tag that is not the policy's canonical package version", () => {
  assert.throws(() => verifyReleaseAssets(policy, [draft()], "v6.47.0.extra"), /tag mismatch/);
  const skewed = structuredClone(policy);
  skewed.package_version = "06.47.0";
  assert.throws(() => verifyReleaseAssets(skewed, [draft()], "v06.47.0"), /tag mismatch/);
});

test("rejects published, missing, or duplicate release records", () => {
  const published = draft();
  published.draft = false;
  assert.throws(() => verifyReleaseAssets(policy, [published], "v6.47.0"), /private draft/);
  assert.throws(() => verifyReleaseAssets(policy, [], "v6.47.0"), /private draft/);
  assert.throws(() => verifyReleaseAssets(policy, [draft(), draft()], "v6.47.0"), /private draft/);
});

test("rejects missing, duplicate, non-uploaded, or mismatched launcher assets", () => {
  const missing = draft();
  missing.assets.pop();
  assert.throws(() => verifyReleaseAssets(policy, [missing], "v6.47.0"), /exactly one uploaded/);

  const duplicate = draft();
  duplicate.assets.push({ ...duplicate.assets[0] });
  assert.throws(() => verifyReleaseAssets(policy, [duplicate], "v6.47.0"), /exactly one uploaded/);

  for (const mutation of [
    (asset) => { asset.state = "open"; },
    (asset) => { asset.size += 1; },
    (asset) => { asset.digest = `sha256:${"0".repeat(64)}`; },
  ]) {
    const release = draft();
    mutation(release.assets[0]);
    assert.throws(() => verifyReleaseAssets(policy, [release], "v6.47.0"), /asset mismatch/);
  }
});
