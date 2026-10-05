'use strict';

const assert = require('node:assert/strict');
const test = require('node:test');
const { requirePublishedRelease } = require('./check-cli-release.cjs');

function completeRelease() {
  return {
    tag_name: 'v0.1.43', draft: false, published_at: '2026-09-29T04:26:24Z',
    assets: [
      'telara_0.1.43_darwin_amd64.tar.gz', 'telara_0.1.43_darwin_arm64.tar.gz',
      'telara_0.1.43_linux_amd64.tar.gz', 'telara_0.1.43_linux_arm64.tar.gz',
      'telara_0.1.43_windows_amd64.zip', 'telara_0.1.43_windows_arm64.zip',
      'telara_checksums.txt', 'telara_checksums.txt.sig',
    ].map(name => ({ name, state: 'uploaded', size: 123 })),
  };
}

test('complete published artifacts admit npm independently of a Homebrew failure', () => {
  assert.equal(requirePublishedRelease(completeRelease(), 'v0.1.43', '0.1.43').length, 8);
});

for (const [name, change] of [
  ['absent release', () => null],
  ['wrong tag', r => ({ ...r, tag_name: 'v0.1.42' })],
  ['draft', r => ({ ...r, draft: true })],
  ['unpublished', r => ({ ...r, published_at: null })],
  ['invalid publication timestamp', r => ({ ...r, published_at: 'not-a-date' })],
]) {
  test(`${name} blocks npm publication`, () => {
    assert.throws(() => requirePublishedRelease(change(completeRelease()), 'v0.1.43', '0.1.43'), /not published/);
  });
}

for (const missing of ['telara_0.1.43_windows_arm64.zip', 'telara_checksums.txt', 'telara_checksums.txt.sig']) {
  test(`missing ${missing} blocks npm publication`, () => {
    const release = completeRelease();
    release.assets = release.assets.filter(asset => asset.name !== missing);
    assert.throws(() => requirePublishedRelease(release, 'v0.1.43', '0.1.43'), /incomplete/);
  });
}

for (const [name, change] of [
  ['unfinished upload', asset => { asset.state = 'new'; }],
  ['empty upload', asset => { asset.size = 0; }],
  ['invalid size', asset => { asset.size = '123'; }],
]) {
  test(`${name} blocks npm publication`, () => {
    const release = completeRelease();
    change(release.assets[0]);
    assert.throws(() => requirePublishedRelease(release, 'v0.1.43', '0.1.43'), /incomplete/);
  });
}

test('duplicate archive names block npm publication', () => {
  const release = completeRelease();
  release.assets.push({ ...release.assets[0] });
  assert.throws(() => requirePublishedRelease(release, 'v0.1.43', '0.1.43'), /incomplete/);
});

test('non-version tag is rejected', () => {
  assert.throws(() => requirePublishedRelease(completeRelease(), 'main', '0.1.43'), /Invalid CLI release tag/);
});

test('version mismatch blocks npm before publication', () => {
  assert.throws(() => requirePublishedRelease(completeRelease(), 'v0.1.43', '0.1.42'), /does not match/);
});

test('missing package version blocks npm before publication', () => {
  assert.throws(() => requirePublishedRelease(completeRelease(), 'v0.1.43'), /does not match/);
});
