# NPX Wrapper Plan: `campfire-mcp`

## Goal

An npm package that lets any Claude Code user (or any MCP-capable agent runtime) add Campfire MCP with zero prerequisites:

```json
{
  "mcpServers": {
    "campfire": {
      "command": "npx",
      "args": ["campfire-mcp"]
    }
  }
}
```

No Go toolchain. No `brew install`. No curl-pipe-sh. Just npm (which Node users already have).

---

## Prior Art

The pattern is well-established. Projects that do this correctly:

- **esbuild** (`esbuild-linux-64`, `esbuild-darwin-arm64`, etc.) — optional platform deps, main package picks the right one at install time
- **turbo** (`turbo-linux-64`, etc.) — same pattern, used by Vercel
- **@biomejs/biome** — single package, downloads at postinstall from GitHub Releases
- **lightningcss** — platform-specific optional deps

Two viable approaches:

### Option A: Optional platform-specific packages (esbuild pattern)

```
campfire-mcp/                    # main package
campfire-mcp-linux-x64/          # platform package
campfire-mcp-linux-arm64/
campfire-mcp-darwin-x64/
campfire-mcp-darwin-arm64/
campfire-mcp-win32-x64/
```

Each platform package ships the binary as a native file. The main package's `bin` entry runs a JS shim that:
1. Finds the installed platform package
2. Executes the binary directly via `child_process.execFileSync`

**Pros:** Works offline after install. No postinstall network requests. npm audit can inspect packages.
**Cons:** More packages to publish and keep in sync with Go releases. Each platform package must be republished per release.

### Option B: Download at postinstall (biome pattern)

Single `campfire-mcp` package. `postinstall` script downloads the correct binary from GitHub Releases for the current platform, drops it in `node_modules/.bin/cf-mcp-bin` (or similar), and writes a shim.

**Pros:** One package to publish. No per-platform packages.
**Cons:** Requires network at install time. Breaks in airgapped environments. Some orgs block postinstall scripts. Harder to audit.

### Recommendation: Option A (optional deps)

Matches what Claude Code users will expect. Works in offline/airgapped setups after initial install. npm's optional dependency mechanism is exactly designed for this — if a platform package isn't available, npm skips it silently rather than failing the install.

---

## Package Structure

### Main package: `campfire-mcp`

```
campfire-mcp/
├── package.json
├── index.js          # shim: find platform binary, exec it
└── README.md
```

`package.json`:
```json
{
  "name": "campfire-mcp",
  "version": "0.1.0",
  "description": "Campfire MCP server for AI agents",
  "bin": {
    "campfire-mcp": "index.js"
  },
  "optionalDependencies": {
    "campfire-mcp-linux-x64": "0.1.0",
    "campfire-mcp-linux-arm64": "0.1.0",
    "campfire-mcp-darwin-x64": "0.1.0",
    "campfire-mcp-darwin-arm64": "0.1.0",
    "campfire-mcp-win32-x64": "0.1.0"
  },
  "repository": {
    "type": "git",
    "url": "https://github.com/campfire-net/campfire"
  },
  "license": "Apache-2.0"
}
```

`index.js`:
```js
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
```

### Platform packages: `campfire-mcp-linux-x64`, etc.

Each is a minimal package that ships just the binary:

```
campfire-mcp-linux-x64/
├── package.json
└── cf-mcp             # the Go binary, committed or generated
```

`package.json`:
```json
{
  "name": "campfire-mcp-linux-x64",
  "version": "0.1.0",
  "description": "Linux x64 binary for campfire-mcp",
  "os": ["linux"],
  "cpu": ["x64"],
  "license": "Apache-2.0"
}
```

The `os` and `cpu` fields cause npm to skip installation on non-matching platforms.

---

## Release Workflow

The Go release pipeline (`.github/workflows/release.yml`) already produces the binaries. Add a step:

1. After building, extract the per-platform `cf-mcp` binary from each `.tar.gz`
2. Copy into the corresponding platform npm package directory
3. Bump all package versions to match the Go release tag
4. `npm publish` each platform package, then `npm publish` the main package

The version number in all npm packages must stay in sync with the Go release tag (mapping `v0.1.2` → `0.1.2`).

A script in `npm/publish.sh` can automate the extract → version bump → publish steps, driven by the release tag from `$GITHUB_REF_NAME`.

---

## Directory Layout in Repo

```
npm/
├── campfire-mcp/
│   ├── package.json
│   ├── index.js
│   └── README.md
├── campfire-mcp-linux-x64/
│   └── package.json
├── campfire-mcp-linux-arm64/
│   └── package.json
├── campfire-mcp-darwin-x64/
│   └── package.json
├── campfire-mcp-darwin-arm64/
│   └── package.json
├── campfire-mcp-win32-x64/
│   └── package.json
└── publish.sh         # extract binaries from dist/, bump versions, npm publish
```

Binaries are NOT committed to the repo. The publish script extracts them from `dist/` at release time.

---

## User Experience After

MCP config:
```json
{
  "mcpServers": {
    "campfire": {
      "command": "npx",
      "args": ["campfire-mcp"]
    }
  }
}
```

Or with Claude Code's `claude mcp add`:
```bash
claude mcp add campfire -- npx campfire-mcp
```

First run: `npx` installs the package + correct platform dep (~5MB), then executes `cf-mcp`. Subsequent runs: cached, instant.

---

## What to Build (Implementation Checklist)

- [ ] `npm/campfire-mcp/package.json` with optionalDependencies
- [ ] `npm/campfire-mcp/index.js` shim
- [ ] `npm/campfire-mcp-{platform}/package.json` for each platform (5 packages)
- [ ] `npm/publish.sh` — extracts binaries from `dist/`, bumps versions, publishes
- [ ] `.github/workflows/release.yml` step: call `npm/publish.sh` after GitHub Release is created
- [ ] npm org/scope decision: publish under `@campfire` scope or unscoped `campfire-mcp`
- [ ] npmjs.com account + `NPM_TOKEN` secret in the repo
