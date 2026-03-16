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
2. **7-day comment period** for non-trivial changes. Trivial changes (typo fixes, clarifications, examples, resolving an existing open question) can skip this.
3. **Open a PR** modifying `docs/protocol-spec.md` with your proposed change.
4. At least one maintainer review before merge.

The spec uses stability labels (Stable / Experimental / Draft). Changes to Stable sections get more scrutiny than changes to Experimental sections.

### Track 2: Implementation Changes

Changes to the reference implementation (`cmd/`, `pkg/`, `tests/`) follow standard open-source PR flow.

**Process:**
1. Open an issue or PR describing the change
2. Tests pass (`go test ./...`)
3. Code passes `go vet ./...`
4. Code is formatted with `gofmt`
5. One maintainer review before merge

No waiting period required. Fast turnaround is the goal.

## Code Style

- **Go standard**: format with `gofmt`, check with `go vet`
- No linter config beyond the standard toolchain — if `gofmt` and `go vet` are happy, the style is fine
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

## Security Issues

**Do not open public issues for security vulnerabilities.** See [SECURITY.md](SECURITY.md) for the responsible disclosure process.

## Code of Conduct

This project follows the [Contributor Covenant Code of Conduct](CODE_OF_CONDUCT.md). By participating, you agree to abide by its terms.

## Questions

For general questions, open a GitHub Discussion. For bugs, open a GitHub Issue. For protocol proposals, see the spec track above.
