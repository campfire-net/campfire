---
title: "[Protocol Proposal] "
labels: protocol
---

Use this template to propose a change to the Campfire protocol spec (`docs/protocol-spec.md`). Read the [contribution guidelines](../../CONTRIBUTING.md) before posting — protocol proposals with sufficient community interest are followed up with a formal PR and 7-day comment period.

---

## Summary

One paragraph: what are you proposing to change or add to the protocol, and why?

## Motivation

What problem does this solve? What use cases become possible (or easier) with this change? What is broken or suboptimal today?

## Affected spec sections

Which sections of `docs/protocol-spec.md` would this change? (e.g., Message envelope, Membership, Filter interface, Beacon, Security Considerations)

## Stability impact

Does this change affect any components currently labeled **Stable**? If yes, explain why the stability tradeoff is worth it.

## Proposed semantics

The technical details of the proposed change. Be precise: what fields change, what are the new semantics, what does an implementation need to do differently?

## Security considerations

Does this change affect the security model? Could it enable spoofing, weaken provenance verification, expose membership data, or affect threshold signature properties? If yes, how is the risk mitigated?

## Recursive composition impact

Does this change affect how campfire-as-member works? Does it preserve child campfire opacity to the parent? Does it break or constrain the recursive composition interface?

## Wire compatibility

Is this a breaking change to the wire format? If yes, what is the migration path?

## Prior art

Similar mechanisms in XMPP, ActivityPub, Matrix, Noise Protocol, Signal, or other protocol projects. What can we learn from how they solved analogous problems?

## Open questions

What aspects of this proposal are still unresolved? What would you want feedback on specifically?
