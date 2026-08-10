const assert = require("node:assert/strict");
const test = require("node:test");
const policy = require("../plugin/engram/bootstrap-targets.json");
const { verifyReleaseAssets } = require("./verify-bootstrap-release-assets.js");
const releaseTag = `v${policy.package_version}`;

function draft() {
  return {
    id: 47,
    tag_name: releaseTag,
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
  assert.equal(verifyReleaseAssets(policy, [draft()], releaseTag), 47);
});

test("rejects a tag that is not the policy's canonical package version", () => {
  assert.throws(() => verifyReleaseAssets(policy, [draft()], `${releaseTag}.extra`), /tag mismatch/);
  const skewed = structuredClone(policy);
  skewed.package_version = `0${policy.package_version}`;
  assert.throws(() => verifyReleaseAssets(skewed, [draft()], `v${skewed.package_version}`), /tag mismatch/);
  const unknown = structuredClone(policy);
  unknown.extra = true;
  assert.throws(() => verifyReleaseAssets(unknown, [draft()], releaseTag), /malformed bootstrap policy/);
});

test("rejects build metadata in release identities", () => {
  const metadata = structuredClone(policy);
  metadata.package_version = `${policy.package_version}+build.1`;
  for (const target of Object.values(metadata.targets)) target.desired.version = metadata.package_version;
  metadata.build_contract.daemon_version_ldflag = `v${metadata.package_version}`;
  assert.throws(() => verifyReleaseAssets(metadata, [draft()], `v${metadata.package_version}`), /malformed bootstrap policy/);
});

test("rejects published, missing, or duplicate release records", () => {
  const published = draft();
  published.draft = false;
  assert.throws(() => verifyReleaseAssets(policy, [published], releaseTag), /private draft/);
  assert.throws(() => verifyReleaseAssets(policy, [], releaseTag), /private draft/);
  assert.throws(() => verifyReleaseAssets(policy, [draft(), draft()], releaseTag), /private draft/);
});

test("rejects missing, duplicate, non-uploaded, or mismatched launcher assets", () => {
  const missing = draft();
  missing.assets.pop();
  assert.throws(() => verifyReleaseAssets(policy, [missing], releaseTag), /exactly one uploaded/);

  const duplicate = draft();
  duplicate.assets.push({ ...duplicate.assets[0] });
  assert.throws(() => verifyReleaseAssets(policy, [duplicate], releaseTag), /exactly one uploaded/);

  for (const mutation of [
    (asset) => { asset.state = "open"; },
    (asset) => { asset.size += 1; },
    (asset) => { asset.digest = `sha256:${"0".repeat(64)}`; },
  ]) {
    const release = draft();
    mutation(release.assets[0]);
    assert.throws(() => verifyReleaseAssets(policy, [release], releaseTag), /asset mismatch/);
  }
});
