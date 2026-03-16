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

  // Download from GitHub Releases with checksum verification
  const crypto = require('crypto');
  const goArch = arch === 'x64' ? 'amd64' : arch;
  const goPlatform = platform;
  const ext = platform === 'win32' ? 'zip' : 'tar.gz';
  const archiveName = `cf_${goPlatform}_${goArch}.${ext}`;
  const baseUrl = 'https://github.com/campfire-net/campfire/releases/latest/download';
  const archiveUrl = `${baseUrl}/${archiveName}`;
  const checksumsUrl = `${baseUrl}/checksums.txt`;
  const archivePath = path.join(cacheDir, archiveName);

  process.stderr.write(`campfire-mcp: downloading ${archiveName}\n`);
  fs.mkdirSync(cacheDir, { recursive: true });

  try {
    // Download archive and checksums
    execSync(`curl -sL "${archiveUrl}" -o "${archivePath}"`, { stdio: 'pipe' });
    const checksums = execSync(`curl -sL "${checksumsUrl}"`, { encoding: 'utf8' });

    // Verify SHA256
    const archiveData = fs.readFileSync(archivePath);
    const actualHash = crypto.createHash('sha256').update(archiveData).digest('hex');
    const expectedLine = checksums.split('\n').find(l => l.includes(archiveName));
    if (!expectedLine) {
      throw new Error(`checksum not found for ${archiveName}`);
    }
    const expectedHash = expectedLine.trim().split(/\s+/)[0];
    if (actualHash !== expectedHash) {
      fs.unlinkSync(archivePath);
      throw new Error(`checksum mismatch: expected ${expectedHash}, got ${actualHash}`);
    }
    process.stderr.write(`campfire-mcp: checksum verified\n`);

    // Extract
    if (platform === 'win32') {
      execSync(`cd "${cacheDir}" && tar -xf "${archivePath}" --strip-components=1 --include='*/cf-mcp.exe'`, { stdio: 'pipe' });
    } else {
      execSync(`tar xzf "${archivePath}" -C "${cacheDir}" --strip-components=1 --wildcards '*/cf-mcp'`, { stdio: 'pipe' });
    }
    fs.unlinkSync(archivePath);
    fs.chmodSync(cachedBin, 0o755);
    if (fs.existsSync(cachedBin)) return cachedBin;
  } catch (e) {
    throw new Error(`campfire-mcp: failed to download/verify binary: ${e.message}\nInstall manually: curl -fsSL https://getcampfire.dev/install.sh | sh`);
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
