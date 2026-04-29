# cf-protocol

`cf-protocol` is the signed-message substrate for campfire. It defines the
wire format, signature algorithm, hop-chain provenance, and DAG primitives
(antecedent links, future/fulfills semantics) that all campfire layers build
on. Protocol-level invariants are frozen at v1.0; see
[COMPATIBILITY.md](COMPATIBILITY.md) for the full freeze commitment.

---

## Stateless Deployment

### When TOFU Is Appropriate — and When It Creates a Security Hole

**Trust On First Use (TOFU)** is campfire's default convention-trust mechanism.
When `cf` first encounters a signed convention declaration from a new signer,
it pins the content-hash / signer-key pair in `~/.cf/pins.json` and uses that
pin for all future comparisons. This is the **TOFU enabled** posture:
trust-on-first-encounter, with subsequent encounters verified against the
initial observation.

TOFU enabled is appropriate when:

- The operator runs `cf` on a persistent filesystem (laptop, server with
  mounted volume, k8s pod with a PVC).
- The pin file survives across process restarts and container redeployments.
- The operator controls the identity key and can revoke or reset pins via
  `cf trust reset`.

**TOFU creates a security hole in stateless-deployment scenarios** where the
filesystem is ephemeral and pins cannot survive across invocations:

- Docker containers without a mounted volume
- AWS Lambda / Azure Functions (cf-functions) / Google Cloud Run
- Browser / Wasm runtimes
- Read-only container images with `/tmp`-only writes

In these environments, every invocation starts with an empty pin store. A
malicious convention declaration substituted between invocations passes TOFU
silently — there is no prior pin to compare against. The second invocation
trusts the forged declaration as if it were the first encounter. This is
indistinguishable from a legitimate first-use event.

**TOFU disabled** is the correct posture for stateless deployments: explicit
pinning required, no implicit trust. An unknown signer's declaration is
rejected outright instead of being silently pinned.

---

### How cf Detects an Ephemeral Filesystem

`cf` detects an ephemeral filesystem using the following heuristics, evaluated
at `PinStore` initialization:

1. **`CF_NO_PINS=1` environment variable** — operator-set flag; the strongest
   signal and the recommended way to communicate stateless intent from a
   deployment manifest (Dockerfile `ENV`, Lambda environment variable, Azure
   Functions application setting).

2. **Write probe** — on first write attempt, `cf` probes whether the pin file
   path is on a tmpfs or a read-only mount. If the probe fails with a
   filesystem error that indicates ephemeral storage (e.g., `EROFS`, writes to
   `/tmp` but not to the configured `CF_HOME`), TOFU is automatically disabled
   for the session with a warning logged to stderr:

   ```
   WARNING: TOFU disabled — pin store path appears to be on an ephemeral
   filesystem. Set CF_NO_PINS=1 to suppress this warning and explicitly
   disable TOFU, or mount a persistent volume at CF_HOME.
   ```

3. **Missing CF_HOME persistence** — if `CF_HOME` resolves to a directory that
   is recreated fresh on each invocation (detected via absence of the identity
   key that was previously initialized), `cf` refuses to operate in TOFU mode
   and surfaces an error directing the operator to either mount persistent
   storage or set `CF_NO_PINS=1`.

---

### TOFU Disable Behavior

When TOFU is disabled (via `CF_NO_PINS=1` or automatic ephemeral-fs detection):

- `cf` refuses all pin write operations. Attempts to pin a new signer result in
  a hard DENY, not a silent accept.
- `cf` refuses all pin read operations. There is no fallback to a stale or
  empty pin store — the pin-check gate is bypassed entirely, and the request
  proceeds to gate evaluation without any TOFU precondition.
- The signing-proxy backend of `cf-session` (which requires a Unix socket) is
  also disabled in stateless deployments — the socket cannot persist across
  Lambda invocations. This is documented separately in the `cf-session`
  convention spec.

**Security posture comparison:**

| Posture | What happens on first encounter | What happens on subsequent encounters |
|---|---|---|
| **TOFU enabled** | Signer key + content-hash pinned; declaration accepted | Pin compared; mismatch → DENY |
| **TOFU disabled** | No pin written; declaration accepted only if explicitly allowed by operator trust policy | No pin to compare; explicit pinning required for any convention trust |

---

### Operator Action Required to Enable Convention Trust Without TOFU

With TOFU disabled, convention trust requires explicit operator action. The
operator must supply trust through one of two mechanisms:

#### 1. Explicit key pinning before deployment

Use `cf trust pin` to pre-populate the pin store on a persistent machine and
mount the result into the stateless deployment:

```bash
# On a persistent machine — pin the convention registry key:
cf trust pin <convention-registry-pubkey>

# Package and mount the pin store into the stateless deployment.
# In Docker:
COPY --from=build /path/to/pins.json /cf-home/pins.json
# In Lambda / Azure Functions: use a mounted EFS / Azure Files share at CF_HOME.
```

The mounted pin store is read-only from the deployment's perspective; all write
attempts are refused (TOFU disabled). The pre-seeded pins act as explicit
operator-supplied trust anchors.

#### 2. Operator-configured trust policy via roll-up config

Define trusted convention signers directly in the roll-up config at
`$CF_HOME/config.toml` under the `[trust]` table. This overrides TOFU
entirely — the operator declares which keys are trusted, and no runtime pinning
is performed:

```toml
[trust]
# Explicit trust policy for stateless deployments.
# Each entry is a hex-encoded Ed25519 public key that is trusted to sign
# convention declarations without a TOFU pin.
pinned_keys = [
  "aaaa...bbbb",   # convention-registry.campfire.net
]
```

Roll-up config is curated by the operator and checked into the deployment
image. It does not change at runtime and is not subject to TOFU semantics.

---

### Security Posture Summary

```
TOFU enabled (default, persistent filesystem):
  trust-on-first-encounter → subsequent encounters verified against initial pin
  → appropriate for interactive developer use and long-lived server deployments

TOFU disabled (stateless deployment):
  no implicit trust → every convention signer must be explicitly pinned or
  declared in operator trust policy before deployment
  → required for Lambda, cf-functions, Wasm, Docker-without-volume
  → set CF_NO_PINS=1 in deployment environment
  → pre-seed pins.json or configure [trust] in config.toml before packaging
```

**Failing to disable TOFU in a stateless environment is a security hole:** an
attacker who can substitute a convention declaration between Lambda invocations
will have it silently trusted on the next cold start, because the empty pin
store treats every invocation as a first encounter.

---

### References

- Design: `docs/design/0.30-design.md` §6 P5 — TOFU placement and HMAC derivation
- Open item: OPEN-026 — Stateless-deployment TOFU disable (Queue B, spec-gap)
- Threat model note: T11 — stateless deployment degradation
- Pin store implementation: `pkg/trust/pin.go`
- Trust CLI: `cf trust pin`, `cf trust unpin`, `cf trust list`, `cf trust reset`
- Compatibility commitment: [COMPATIBILITY.md](COMPATIBILITY.md)
