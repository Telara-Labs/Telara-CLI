'use strict';

// npm's installer downloads these archives by the package's exact version.
// A failed Homebrew update is harmless to that path; missing GitHub artifacts
// are not, so they must block publishing the npm package.
function requirePublishedRelease(release, tag, packageVersion) {
  if (!/^v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/.test(tag)) {
    throw new Error(`Invalid CLI release tag: ${tag}`);
  }
  const version = tag.slice(1);
  if (packageVersion !== version) {
    throw new Error(`npm package version ${packageVersion} does not match CLI release ${tag}`);
  }
  if (!release || release.tag_name !== tag || release.draft !== false ||
      !release.published_at || !Number.isFinite(Date.parse(release.published_at))) {
    throw new Error(`CLI release ${tag} is not published`);
  }
  const expected = ['darwin', 'linux', 'windows'].flatMap(os =>
    ['amd64', 'arm64'].map(arch =>
      `telara_${version}_${os}_${arch}.${os === 'windows' ? 'zip' : 'tar.gz'}`));
  expected.push('telara_checksums.txt', 'telara_checksums.txt.sig');
  for (const name of expected) {
    const matches = (release.assets || []).filter(asset => asset.name === name);
    if (matches.length !== 1 || matches[0].state !== 'uploaded' ||
        !Number.isFinite(matches[0].size) || matches[0].size <= 0) {
      throw new Error(`CLI release ${tag} is incomplete: ${name} must be uploaded and nonempty`);
    }
  }
  return expected;
}

module.exports = { requirePublishedRelease };
