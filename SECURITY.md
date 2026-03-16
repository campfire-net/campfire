# Security Policy

## Reporting a Vulnerability

**Do not open a public GitHub issue for security vulnerabilities.**

If you discover a security vulnerability in the Campfire protocol spec or reference implementation, please report it by email:

**security@3dl.dev**

Include in your report:
- A description of the vulnerability and its potential impact
- Steps to reproduce or a proof-of-concept (if applicable)
- The component affected (protocol spec, specific package, CLI, MCP server)
- Any suggested mitigations you've identified

You will receive an acknowledgment within 48 hours. We aim to provide an initial assessment within 7 days and a fix or mitigation plan within 30 days, depending on severity and complexity.

## Scope

The security model for Campfire is defined in the [protocol spec](docs/protocol-spec.md) under **Security Considerations**. In scope for security reports:

- Identity spoofing — forging message signatures or provenance hops
- Membership hash manipulation — lying about campfire membership in provenance
- Threshold signature vulnerabilities — breaking the M-of-N signing guarantee
- Key material exposure — leaking private keys or key shares through the CLI, MCP server, or transport layer
- CBOR deserialization vulnerabilities — malformed inputs causing incorrect behavior or crashes
- Provenance chain verification bypass — accepting invalid chains as valid

Out of scope:
- Denial of service via resource exhaustion (campfire nodes are not public infrastructure)
- Issues in transitive Go dependencies not directly used by campfire
- Protocol open questions already documented in the spec (message ordering, TTL, key rotation) — these are known gaps, not vulnerabilities

## Disclosure Policy

We follow coordinated disclosure. Once a fix is ready:
1. We will notify you and share the fix for review
2. We will publish a security advisory on GitHub
3. We will release a patched version
4. You are free to publish your findings after the patched release is available

We will credit reporters in the security advisory unless you prefer to remain anonymous.

## Supported Versions

During the Draft v0.1 phase, only the latest release receives security fixes. Once the protocol reaches v1.0, a longer support window will be defined.
