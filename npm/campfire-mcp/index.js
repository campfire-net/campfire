#!/usr/bin/env node
'use strict';

const { execFileSync } = require('child_process');
const path = require('path');
const os = require('os');

function getBinaryPath() {
  const platform = os.platform(); // 'linux', 'darwin', 'win32'
  const arch = os.arch();         // 'x64', 'arm64'

  const pkgMap = {
    'linux-x64':    'campfire-mcp-linux-x64',
    'linux-arm64':  'campfire-mcp-linux-arm64',
    'darwin-x64':   'campfire-mcp-darwin-x64',
    'darwin-arm64': 'campfire-mcp-darwin-arm64',
    'win32-x64':    'campfire-mcp-win32-x64',
  };

  const key = `${platform}-${arch}`;
  const pkgName = pkgMap[key];
  if (!pkgName) {
    throw new Error(`campfire-mcp: unsupported platform ${key}`);
  }

  try {
    const pkgDir = path.dirname(require.resolve(`${pkgName}/package.json`));
    const bin = platform === 'win32'
      ? path.join(pkgDir, 'cf-mcp.exe')
      : path.join(pkgDir, 'cf-mcp');
    return bin;
  } catch {
    throw new Error(
      `campfire-mcp: platform package ${pkgName} is not installed.\n` +
      `Run: npm install ${pkgName}`
    );
  }
}

try {
  const bin = getBinaryPath();
  execFileSync(bin, process.argv.slice(2), { stdio: 'inherit' });
} catch (err) {
  process.stderr.write(err.message + '\n');
  process.exit(1);
}
