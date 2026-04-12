# SSH-agent Signing Notes: Go crypto/ssh/agent Wire Format for ed25519 Keys

## 1. Question

Does `(*agent.Agent).Sign(key ssh.PublicKey, data []byte) (*ssh.Signature, error)` for an
ed25519 key return:

- **(A)** A bare 64-byte ed25519 signature inside `Signature.Blob`, OR
- **(B)** An SSH wire-format blob that must be unwrapped

## 2. Setup — Methodology

**In-process keyring (no external ssh-agent binary required)**

The test in `pkg/identity/sshagent_spike_test.go` uses `agent.NewKeyring()` — the
in-process implementation bundled with `golang.org/x/crypto/ssh/agent`. This exercises
exactly the same code path as a real external ssh-agent because:

- The external path serializes/deserializes over a Unix socket, but `client.SignWithFlags`
  calls `ssh.Unmarshal(msg.SigBlob, &sig)` before returning — the client always unwraps.
- The in-process keyring's `Sign` calls `signer.Sign` and returns the `*ssh.Signature`
  directly, with no wire encoding involved.

Both paths produce the same `*ssh.Signature` value.

**Source reading (corroborating evidence)**

Three files from `golang.org/x/crypto@v0.41.0/ssh/` were traced:

| File | Key line |
|------|----------|
| `agent/server.go:142` | `SigBlob: ssh.Marshal(sig)` — server encodes on the wire |
| `agent/client.go:459` | `ssh.Unmarshal(msg.SigBlob, &sig)` — client decodes before returning |
| `ssh/keys.go:1161–1164` | `return &Signature{Format: algorithm, Blob: signature}` — for ed25519, `signature` is the raw 64-byte output of `crypto/ed25519.Sign`; no re-encoding applied |
| `ssh/keys.go:735` | `ed25519.Verify(ed25519.PublicKey(k), b, sig.Blob)` — stdlib verify takes `Blob` directly |

## 3. Observed Behavior

Test run (`go test ./pkg/identity/ -run TestSSHAgentSignWireFormat -v`):

```
agent Signature.Format : ssh-ed25519
agent Signature.Blob   : len=64  hex=dc6f7bb1...993201
direct ed25519.Sign    : len=64  hex=dc6f7bb1...993201
RESULT: Signature.Blob == bare ed25519.Sign output  =>  ANSWER (A)
ed25519.Verify(pub, data, agentSig.Blob) PASSED — Blob is bare 64 bytes
--- PASS: TestSSHAgentSignWireFormat
```

- `Signature.Blob` length: 64 bytes (exactly `ed25519.SignatureSize`)
- `Signature.Blob` hex: identical to `ed25519.Sign(priv, data)`
- `ed25519.Verify(pub, data, agentSig.Blob)`: PASS — the standard library verifies it directly

## 4. Comparison

| Property | agent.Sign result | Direct ed25519.Sign |
|----------|------------------|---------------------|
| Length of Blob | 64 bytes | 64 bytes |
| Hex values | identical | identical |
| Passes ed25519.Verify directly | yes | yes |
| Needs SSH wire-format decoding | no | N/A |

## 5. Answer

**Answer: (A)**

`agent.Sign` for an ed25519 key returns a `*ssh.Signature` where:
- `Format` = `"ssh-ed25519"`
- `Blob` = the bare 64-byte ed25519 signature

No further unwrapping is needed. The SSH wire-format encoding (`ssh.Marshal(sig)`) is
used on the transport layer between the agent client and server, but it is transparently
decoded by `client.SignWithFlags` before the `*ssh.Signature` is returned to the caller.

To extract the 32-byte public key and use the signature with standard Go crypto:

```go
sig, err := agentClient.Sign(sshPub, data)
// sig.Blob is directly usable:
ok := ed25519.Verify(rawPub, data, sig.Blob)  // no unwrapping needed
```

## 6. 1Password Caveat

1Password SSH agent (op-ssh-sign) wraps keys in the SSH agent protocol and honors the
same wire format. The 1Password agent returns signatures through the standard
`SSH_AGENTC_SIGN_REQUEST` / `SSH_AGENT_SIGN_RESPONSE` exchange, so the same
`client.SignWithFlags` path applies — `Signature.Blob` will be the bare 64-byte
ed25519 output, identical to a plain ssh-agent.

**However**, 1Password may require user confirmation (tap-to-sign). In automated
workflows, this means:
- Non-interactive processes (CI, agent daemons) cannot use 1Password's SSH agent
  for signing unless a "touch confirmation" is disabled in 1Password settings.
- The campfire SigningBackend implementation should surface a clear error
  (`ErrUserConfirmationRequired` or similar) when Sign times out due to missing
  user interaction, rather than hanging indefinitely.

The wire-format answer itself is unaffected by 1Password — only availability and
interactivity differ.

## 7. References

- `golang.org/x/crypto/ssh/agent` client.go — `SignWithFlags` (line 444): unwraps wire blob
- `golang.org/x/crypto/ssh/agent` server.go — `processRequest` (line 142): wraps with `ssh.Marshal`
- `golang.org/x/crypto/ssh` keys.go — `wrappedSigner.SignWithAlgorithm` (line 1110): ed25519 path puts raw bytes in `Signature.Blob`
- `golang.org/x/crypto/ssh` keys.go — `ed25519PublicKey.Verify` (line 727): passes `sig.Blob` directly to `ed25519.Verify`
- Test: `pkg/identity/sshagent_spike_test.go` — `TestSSHAgentSignWireFormat`
- IETF draft-miller-ssh-agent-00 §2.6.2 — sign request/response protocol
