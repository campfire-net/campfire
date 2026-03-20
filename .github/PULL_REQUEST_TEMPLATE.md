## What does this change?

A clear description of the change and the problem it solves. Reference related issues with `Fixes #N` or `Closes #N`.

## Is this a protocol change or an implementation change?

- [ ] Protocol change (modifies `docs/protocol-spec.md` — open an issue first)
- [ ] Implementation change (modifies `cmd/`, `pkg/`, `tests/`, or other non-spec files)

## How to test

Steps to verify the change works as expected. Include commands, sample configs, or test scenarios.

```bash
# example
go test ./pkg/message/...
```

## Checklist

- [ ] Tests pass: `go test ./...`
- [ ] No vet warnings: `go vet ./...`
- [ ] Code is formatted: `gofmt -l .` returns nothing
- [ ] Commits include DCO sign-off: `git commit -s`
- [ ] Protocol spec changes: issue opened first
- [ ] New features include tests
- [ ] Bug fixes include a regression test
