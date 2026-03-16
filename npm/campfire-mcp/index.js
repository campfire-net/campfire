#!/usr/bin/env node
'use strict';

const { execFileSync, execSync } = require('child_process');
const path = require('path');
const fs = require('fs');
const os = require('os');

function getBinaryPath() {
  const platform = os.platform();
  const arch = os.arch();

  const pkgMap = {
    'linux-x64':    '@campfire-net/campfire-mcp-linux-x64',
    'linux-arm64':  '@campfire-net/campfire-mcp-linux-arm64',
    'darwin-x64':   '@campfire-net/campfire-mcp-darwin-x64',
    'darwin-arm64': '@campfire-net/campfire-mcp-darwin-arm64',
    'win32-x64':    '@campfire-net/campfire-mcp-win32-x64',
  };

  const key = `${platform}-${arch}`;
  const pkgName = pkgMap[key];
  if (!pkgName) {
    throw new Error(`campfire-mcp: unsupported platform ${key}`);
  }

  // Try platform package first
  try {
    const pkgDir = path.dirname(require.resolve(`${pkgName}/package.json`));
    const bin = platform === 'win32'
      ? path.join(pkgDir, 'cf-mcp.exe')
      : path.join(pkgDir, 'cf-mcp');
    if (fs.existsSync(bin)) return bin;
  } catch {}

  // Fallback: check cache
  const cacheDir = path.join(os.homedir(), '.cache', 'campfire-mcp');
  const cachedBin = path.join(cacheDir, platform === 'win32' ? 'cf-mcp.exe' : 'cf-mcp');
  if (fs.existsSync(cachedBin)) return cachedBin;

  // Download from GitHub Releases
  const goArch = arch === 'x64' ? 'amd64' : arch;
  const goPlatform = platform;
  const ext = platform === 'win32' ? 'zip' : 'tar.gz';
  const url = `https://github.com/campfire-net/campfire/releases/latest/download/cf_${goPlatform}_${goArch}.${ext}`;

  process.stderr.write(`campfire-mcp: downloading binary from ${url}\n`);
  fs.mkdirSync(cacheDir, { recursive: true });

  try {
    if (platform === 'win32') {
      execSync(`curl -sL "${url}" -o "${cacheDir}/cf.zip" && cd "${cacheDir}" && tar -xf cf.zip cf-mcp.exe`, { stdio: 'pipe' });
    } else {
      execSync(`curl -sL "${url}" | tar xz -C "${cacheDir}" --strip-components=1 --wildcards '*/cf-mcp'`, { stdio: 'pipe' });
    }
    fs.chmodSync(cachedBin, 0o755);
    if (fs.existsSync(cachedBin)) return cachedBin;
  } catch (e) {
    throw new Error(`campfire-mcp: failed to download binary: ${e.message}\nInstall manually: curl -fsSL https://getcampfire.dev/install.sh | sh`);
  }

  throw new Error('campfire-mcp: could not find or download binary');
}

try {
  const bin = getBinaryPath();
  execFileSync(bin, process.argv.slice(2), { stdio: 'inherit' });
} catch (err) {
  process.stderr.write(err.message + '\n');
  process.exit(1);
}
