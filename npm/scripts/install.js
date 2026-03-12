#!/usr/bin/env node

'use strict';

const https = require('https');
const fs = require('fs');
const path = require('path');
const os = require('os');
const { execSync } = require('child_process');

// Read version from package.json, allow override via env var
const pkg = require('../package.json');
const version = process.env.TELARA_VERSION || pkg.version;

const PRIMARY_BASE_URL = 'https://get.telara.dev/download';
const FALLBACK_BASE_URL = 'https://github.com/Telara-Labs/Telara-CLI/releases/download';
const MANUAL_INSTALL_URL = 'https://docs.telara.dev/mcp-clients/cli';

function getPlatformInfo() {
  const platform = process.platform;
  const arch = process.arch;

  let os_name;
  switch (platform) {
    case 'darwin':
      os_name = 'darwin';
      break;
    case 'linux':
      os_name = 'linux';
      break;
    case 'win32':
      os_name = 'windows';
      break;
    default:
      throw new Error(
        `Unsupported platform: ${platform}\n` +
        `Please install manually: ${MANUAL_INSTALL_URL}`
      );
  }

  let arch_name;
  switch (arch) {
    case 'x64':
      arch_name = 'amd64';
      break;
    case 'arm64':
      arch_name = 'arm64';
      break;
    default:
      throw new Error(
        `Unsupported architecture: ${arch}\n` +
        `Please install manually: ${MANUAL_INSTALL_URL}`
      );
  }

  return { os_name, arch_name };
}

function getArchiveFilename(version, os_name, arch_name) {
  // version field in package.json does not include the "v" prefix
  // archive filenames use the bare version number (e.g. telara_0.1.0_darwin_amd64.tar.gz)
  const bare_version = version.replace(/^v/, '');

  if (os_name === 'windows') {
    return `telara_${bare_version}_windows_${arch_name}.zip`;
  }
  return `telara_${bare_version}_${os_name}_${arch_name}.tar.gz`;
}

function getBinPath() {
  const bin_dir = path.join(__dirname, '..', 'bin');
  const bin_name = process.platform === 'win32' ? 'telara.exe' : 'telara';
  return { bin_dir, bin_path: path.join(bin_dir, bin_name) };
}

function isCorrectVersionInstalled(bin_path) {
  if (!fs.existsSync(bin_path)) {
    return false;
  }
  try {
    const output = execSync(`"${bin_path}" version`, {
      timeout: 5000,
      stdio: ['ignore', 'pipe', 'ignore'],
    }).toString().trim();
    const bare_version = version.replace(/^v/, '');
    return output.includes(bare_version);
  } catch (_) {
    return false;
  }
}

function download(url, dest) {
  return new Promise((resolve, reject) => {
    const file = fs.createWriteStream(dest);

    const request = (target_url) => {
      https.get(target_url, (res) => {
        if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location) {
          // Follow redirect
          res.resume();
          request(res.headers.location);
          return;
        }

        if (res.statusCode !== 200) {
          file.close();
          fs.unlink(dest, () => {});
          reject(new Error(`Download failed with HTTP ${res.statusCode} from ${target_url}`));
          return;
        }

        res.pipe(file);
        file.on('finish', () => {
          file.close(resolve);
        });
      }).on('error', (err) => {
        file.close();
        fs.unlink(dest, () => {});
        reject(err);
      });
    };

    request(url);
  });
}

function extractTarGz(archive_path, extract_dir) {
  execSync(`tar -xzf "${archive_path}" -C "${extract_dir}"`, { stdio: 'inherit' });
}

function extractZip(archive_path, extract_dir) {
  // Use PowerShell Expand-Archive on Windows
  execSync(
    `powershell -NoProfile -NonInteractive -Command "Expand-Archive -Force -Path '${archive_path}' -DestinationPath '${extract_dir}'"`,
    { stdio: 'inherit' }
  );
}

async function downloadWithFallback(filename, version, dest) {
  const tag = `v${version.replace(/^v/, '')}`;
  const primary_url = `${PRIMARY_BASE_URL}/${tag}/${filename}`;
  const fallback_url = `${FALLBACK_BASE_URL}/${tag}/${filename}`;

  console.log(`Downloading telara v${version.replace(/^v/, '')}...`);

  try {
    await download(primary_url, dest);
    return;
  } catch (primary_err) {
    console.log(`Primary download failed (${primary_err.message}), trying fallback...`);
  }

  try {
    await download(fallback_url, dest);
  } catch (fallback_err) {
    throw new Error(
      `Both download sources failed.\n` +
      `  Primary:  ${primary_url}\n` +
      `  Fallback: ${fallback_url}\n` +
      `  Last error: ${fallback_err.message}`
    );
  }
}

async function main() {
  const { os_name, arch_name } = getPlatformInfo();
  const filename = getArchiveFilename(version, os_name, arch_name);
  const { bin_dir, bin_path } = getBinPath();

  // Skip download if correct version is already installed
  if (isCorrectVersionInstalled(bin_path)) {
    console.log(`telara v${version.replace(/^v/, '')} is already installed.`);
    return;
  }

  // Ensure bin directory exists
  if (!fs.existsSync(bin_dir)) {
    fs.mkdirSync(bin_dir, { recursive: true });
  }

  const tmp_dir = fs.mkdtempSync(path.join(os.tmpdir(), 'telara-install-'));
  const archive_path = path.join(tmp_dir, filename);

  try {
    await downloadWithFallback(filename, version, archive_path);

    console.log('Extracting...');
    if (os_name === 'windows') {
      extractZip(archive_path, tmp_dir);
    } else {
      extractTarGz(archive_path, tmp_dir);
    }

    const extracted_bin_name = os_name === 'windows' ? 'telara.exe' : 'telara';
    const extracted_bin = path.join(tmp_dir, extracted_bin_name);

    if (!fs.existsSync(extracted_bin)) {
      throw new Error(`Expected binary not found after extraction: ${extracted_bin}`);
    }

    // Move binary into place — use copy+delete to handle cross-filesystem moves
    try {
      fs.renameSync(extracted_bin, bin_path);
    } catch (_) {
      fs.copyFileSync(extracted_bin, bin_path);
      fs.unlinkSync(extracted_bin);
    }

    // Make executable on unix
    if (os_name !== 'windows') {
      fs.chmodSync(bin_path, 0o755);
    }

    console.log(`telara installed successfully.`);
    console.log('');
    console.log('Get started:');
    console.log('  1. Generate a token at https://app.telara.dev/settings?tab=developer');
    console.log('  2. telara login --token <your-token>');
    console.log('  3. telara setup claude-code');
  } finally {
    // Clean up temp directory
    try {
      fs.rmSync(tmp_dir, { recursive: true, force: true });
    } catch (_) {
      // Non-critical cleanup failure — ignore
    }
  }
}

main().then(() => {
  process.exit(0);
}).catch((err) => {
  console.error('');
  console.error('telara install failed: ' + err.message);
  console.error('');
  console.error('Install manually:');
  console.error('  macOS/Linux: curl -fsSL https://get.telara.dev/install.sh | sh');
  console.error('  Windows:     irm https://get.telara.dev/windows | iex');
  console.error('  More options: ' + MANUAL_INSTALL_URL);
  console.error('');
  process.exit(1);
});
