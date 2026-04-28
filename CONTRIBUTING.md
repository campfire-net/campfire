# Contributing to Campfire

Thank you for your interest in contributing. Campfire is a protocol project — the spec is the product, and the reference implementation exists to prove the spec works. Both are open for contribution.

## Developer Certificate of Origin (DCO)

Campfire uses the DCO instead of a CLA. Every commit must include a `Signed-off-by` line certifying that you have the right to submit the contribution under the Apache 2.0 license.

Add the sign-off with:

```
git commit -s -m "your commit message"
```

This appends `Signed-off-by: Your Name <your@email.com>` to the commit message. By including this line, you certify the following:

> By making a contribution to this project, I certify that:
>
> (a) The contribution was created in whole or in part by me and I have the right to submit it under the open source license indicated in the file; or
>
> (b) The contribution is based upon previous work that, to the best of my knowledge, is covered under an appropriate open source license and I have the right under that license to submit that work with modifications, whether created in whole or in part by me, under the same open source license (unless I am permitted to submit under a different license), as indicated in the file; or
>
> (c) The contribution was provided directly to me by some other person who certified (a), (b) or (c) and I have not modified it.
>
> (d) I understand and agree that this project and the contribution are public and that a record of the contribution (including all personal information I submit with it, including my sign-off) is maintained indefinitely and may be redistributed consistent with this project or the open source license(s) involved.

Contributors retain copyright on their contributions. The DCO does not transfer copyright to Third Division Labs.

## How to Contribute

### Fork, Branch, Pull Request

1. Fork the repository on GitHub
2. Create a branch from `main` with a descriptive name (`fix/beacon-signature-validation`, `feat/mdns-transport`)
3. Make your changes
4. Run the test suite: `go test ./...`
5. Run `go vet ./...` and `gofmt -l .` — fix any issues
6. Commit with sign-off: `git commit -s`
7. Open a pull request against `main`

Keep pull requests focused. One change per PR is easier to review and faster to merge.

## Two-Track Contribution Model

Campfire has two separate tracks with different process requirements. Know which track your contribution falls into before you start.

### Track 1: Protocol Spec Changes

The protocol spec (`docs/protocol-spec.md`) is the source of truth. Changes to the spec affect everyone who implements or relies on the protocol, so they require more process.

**What requires the spec track:**
- New primitives or operations
- Changes to message envelope structure or provenance chain format
- Membership semantics or eviction rules
- Filter interface or optimization contract
- Beacon structure or discovery semantics
- Security model or identity system
- Breaking changes to any wire format or protocol behavior

**Process:**
1. **Open an issue first.** Describe the problem you're solving, the proposed change, and why you think it's the right approach. Include any relevant prior art or discussion.
2. **Open a PR** modifying `docs/protocol-spec.md` with your proposed change.
3. At least one maintainer review before merge.

During the Draft phase, the spec is maintained by Third Division Labs. We welcome proposals and will consider community input, but reserve the right to evolve the spec based on implementation experience. Formal comment periods will be introduced when the protocol reaches stability.

### Track 2: Implementation Changes

Changes to the reference implementation (`cmd/`, `pkg/`, `tests/`) follow standard open-source PR flow.

**Process:**
1. Open an issue or PR describing the change
2. Tests pass (`go test ./...`)
3. Code passes `go vet ./...`
4. Code is formatted with `gofmt`
5. One maintainer review before merge

No waiting period required. Fast turnaround is the goal.

## Cross-Layer Import Policy

The implementation is organized into four layers enforced by `depguard` (configured in `.golangci.yml`):

| Layer | Packages | Description |
|-------|----------|-------------|
| L1 | `pkg/protocol`, substrate packages | Campfire substrate: message envelope, signatures, hop chain, transports |
| L2 | `pkg/convention` | Convention machinery: parser, executor, dispatcher, server |
| L3 | `pkg/trust`, `pkg/naming`, other convention implementations | RFC convention implementations |
| L4 | `cmd/cf`, `cmd/cf-mcp`, `cmd/cf-functions` | Deployment binaries |

**The rule:** lower layers must not import higher layers. Specifically:

- **B1:** `pkg/protocol` (L1) must not import `pkg/convention` (L2).
- **B2:** `pkg/convention` (L2) must not import `pkg/naming`, `pkg/trust`, or other L3 packages.
- **B3:** L3 packages must not import `pkg/convention` internal sub-packages (`delegation`, `declarations`); use only the public `pkg/convention` API.

### Integration-test exemption

Test files (`*_test.go`) may cross layers for integration and end-to-end tests. For example, `pkg/protocol` integration tests legitimately set up convention servers to test the full stack. This exemption is enforced in `.golangci.yml`: depguard boundary rules apply only to non-test files.

### Current tracked allowances

These are known layer-boundary crossings that are permitted while the corresponding refactor is in progress. Every allowance requires a spec-section citation.

| File | Import | Reason | Spec citation | Status |
|------|--------|--------|--------------|--------|
| `pkg/convention/seed.go` | `pkg/naming` | Uses `naming.TagNamePrefix`, `naming.TagPrefix` constants for convention tag registration. Pending migration of `seed.go` to L3 (`cf-convention-extension`) per §4.3. | design v2 §4.3, OPEN-018 | Pending §4.3 migration |
| `pkg/convention/parser.go` | `pkg/naming` | Uses `naming.TagPrefix` in the parser's deny list for naming-reserved tag prefixes. Same migration path as `seed.go`; alternatively, constants move to `pkg/protocol/internal/tagspec` per §4.1. | design v2 §4.1, §4.3, OPEN-018 | Pending §4.3 migration or §4.1 tagspec extraction |
| `pkg/store/aztable` | `pkg/convention` | Implements `convention.DispatchStore` against Azure Table Storage. This is a concrete store backend for the convention system, not a layer violation. The store package is infrastructure that supports L2. | design v2 §4 (store as L2 infrastructure) | Permanent allowance |

### How to add a new exemption

If your PR requires an import that violates one of the layer boundaries above:

1. **Exhaust alternatives first.** Can you use an interface? Can the constant or type move to a lower layer? Can the dependency direction be reversed?

2. **If the crossing is justified**, add a file-level exemption to `.golangci.yml` and a row to the tracked allowances table above. Every exemption requires:
   - A citation of the spec section that justifies the crossing (e.g., "design v2 §4.3 — pending migration").
   - A clear migration path or justification for why this is a permanent allowance.
   - A linked issue or work item tracking the migration (if it is temporary).

3. **Submit the change to `.golangci.yml` and `CONTRIBUTING.md` in the same PR** as the code change that requires the exemption. Do not add an exemption without a corresponding code explanation.

Unexempted boundary violations fail CI (the `Lint` job runs `golangci-lint run --fast-only`).

## Code Style

- **Go standard**: format with `gofmt`, check with `go vet`
- Run `golangci-lint run --fast-only` before submitting — depguard enforces layer boundaries
- Keep functions small and focused
- Prefer clarity over cleverness
- Comments explain why, not what

## Testing

Run the full test suite before submitting:

```bash
go test ./...
```

For integration tests that require multiple agents:

```bash
go test ./tests/...
```

New features should include tests. Bug fixes should include a test that would have caught the bug.

## Versioning Policy

Campfire uses two go modules with independent semver cadences:

- **`cf-protocol`** — long-term wire stability. The v1.0 wire format is
  intended to remain the production major indefinitely. Reserved-op LIST
  additions are **major** bumps (not minor) because consumers may rely on
  the list being complete.

- **`cf-conventions`** — backward-compatible minors within a major; majors
  are expected as layer-3 wire formats evolve. cf-authority, cf-discovery,
  and other L3 packages freeze their wire formats at `cf-conventions` major
  events.

**When you change `cf-protocol`'s exported surface**, CI will tell you if
`cf-conventions`'s floor needs a bump. Check `cf-conventions/floor.txt` and
run `bash scripts/check-floor.sh` to verify.

**When you change `cf-conventions`'s wire format**, decide: is this a minor
(backward-compatible addition) or a major (breaking change)? Breaking changes
ship in a new major branch. See `cf-conventions/COMPATIBILITY.md` for the
full policy.

**Do not pin a minor** in consumer `go.mod` files — pin at major only.
Pinning a minor over-constrains MVS and will cause spurious incompatibility
errors in multi-consumer binaries.

See `cf-conventions/COMPATIBILITY.md` and `cf-protocol/COMPATIBILITY.md`
for the full versioning policy and compatibility matrix.

## Security Issues

**Do not open public issues for security vulnerabilities.** See [SECURITY.md](SECURITY.md) for the responsible disclosure process.

## Code of Conduct

This project follows the [Contributor Covenant Code of Conduct](CODE_OF_CONDUCT.md). By participating, you agree to abide by its terms.

## Questions

For general questions, open a GitHub Discussion. For bugs, open a GitHub Issue. For protocol proposals, see the spec track above.
