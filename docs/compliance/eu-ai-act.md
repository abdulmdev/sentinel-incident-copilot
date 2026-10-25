# EU AI Act: where Sentinel Guard produces evidence

This maps the AI Act's record-keeping, human-oversight and deployer obligations to what Sentinel Guard does. It covers Regulation (EU) 2024/1689, as amended by Regulation (EU) 2026/1744 (the "Digital Omnibus on AI"). The obligations are paraphrased; the authoritative text is the [Official Journal](https://eur-lex.europa.eu/eli/reg/2024/1689/oj).

> **Not legal advice.** These obligations apply to **high-risk** AI systems: those in Annex III, or safety components of products under Annex I. An agent built for something else is usually not high-risk, and then none of this is mandatory, though it is still good practice. One Annex III category matters for agents that change infrastructure. Point 2 covers AI systems intended as **safety components in the management and operation of critical digital infrastructure** (as defined in Directive (EU) 2022/2557), road traffic, and the supply of water, gas, heating or electricity. Have counsel decide whether that describes your system.

## When it applies

Article 113 as amended by Regulation (EU) 2026/1744, in force since 27 July 2026, sets these dates for the obligations below (Chapter III, Sections 1 to 3):
- **2 December 2027** for high-risk systems under Annex III;
- **2 August 2028** for those under Annex I (safety components of regulated products).

Before the amendment, both dates were 2 August 2026.

## Article 12: record-keeping (providers design it in)

| Obligation (paraphrased) | Sentinel Guard |
|---|---|
| **12(1)** High-risk systems must technically allow automatic recording of events (logs) over the system's lifetime | The guard records every agent action automatically, from proposal to execution, in its own signed, hash-chained log. In Kubernetes, the admission webhook sees every request from an agent identity, so an agent can't skip the log by acting directly. Agents outside Kubernetes are logged when they go through the guard (SDKs, MCP proxy); route their credentials through an executor that redeems capabilities, so acting without the guard isn't possible ([ADR-0012](../adr/0012-sdks-and-the-mcp-proxy.md) §4) |
| **12(2)(a)** Logs should support identifying situations that may present a risk, or a substantial modification | Blocked attempts (with the policy reason), denials, expiries and every policy version are events or signed records. A policy change is visible as a new version that later requests name |
| **12(2)(b)** …facilitating post-market monitoring (Art. 72) | The evidence export, with its per-agent oversight summary, and the outcome metrics |
| **12(2)(c)** …monitoring the operation of the system by deployers (Art. 26(5)) | The guardian metrics and alert rules ([README](README.md#guardian-monitoring)) |

The Act does not prescribe a log format or integrity mechanism. The guard's signatures and offline verification go beyond the text. They make the logs credible to a third party, who doesn't have to trust whoever holds them.

## Article 14: human oversight

| Obligation (paraphrased) | Sentinel Guard |
|---|---|
| **14(1)** Design the system, including human-machine interface tools, so people can effectively oversee it while it is in use | Oversight comes before the action, not after: each change waits for a person. People decide with tools they already use: `kubectl sentinel requests` / `approve` / `deny`, or Sentinel's console (through the gateway as a delegate) |
| **14(3)** Oversight measures commensurate with risk, autonomy and context, built in by the provider or implemented by the deployer | Per-action policy: `risk`, and `autonomy` (`deny`, `approve`, `auto` when the guard itself observes the preconditions, `observe` for shadow rollout). Hard limits can't be approved at all: protected namespaces, replica bounds |
| **14(4)(a)** People can understand the system's capacities and limitations, monitor its operation, and detect anomalies and unexpected performance | Each request shows the guard's own precondition observations, which may differ from what the agent claimed. Drift alerts flag an agent whose blocks, denials or need for approval change |
| **14(4)(b)** People remain aware of automation bias | The approver sees the concrete change (`replicas: 3 → 0`, `amount: 4200.00 EUR`) and what the guard observed, not only the agent's reasoning. The `SentinelGuardHumanOverrides` alert also catches the reverse: an agent most of whose requests people reject. Approvals made without looking can't be detected by a tool |
| **14(4)(c)** People can correctly interpret the output | Human-readable changes, risk tier, reasons, intent; the full request with `kubectl sentinel describe` |
| **14(4)(d)** People can decide not to use the system, or disregard, override or reverse its output | Deny a request; set an action's autonomy to `deny`; any approval is for one execution only |
| **14(4)(e)** People can intervene or interrupt the system through a "stop" button or similar procedure that halts it in a safe state | Scale the guard to zero: every agent's change is refused (fail closed), while humans, CI and controllers are unaffected. This is verified on a real API server by the e2e suite. For one action type, set its autonomy to `deny` |
| **14(5)** Separate verification by two people (only for remote biometric identification, Annex III point 1(a)) | Not applicable to the guard's typical use. Each request takes one approver today; *N*-of-*M* approvals are on the roadmap |

## Article 19: automatically generated logs (providers)

| Obligation (paraphrased) | Sentinel Guard |
|---|---|
| **19(1)** Providers keep the Art. 12(1) logs under their control for a period appropriate to the intended purpose, of at least six months, unless other Union or national law (notably data protection) provides otherwise | `ActionRequest` objects can't be deleted, even by cluster-admin. Evidence bundles can be archived to write-once storage, each verified to extend the last ([README](README.md#archiving-retention)) |

## Article 26: obligations of deployers

| Obligation (paraphrased) | Sentinel Guard |
|---|---|
| **26(1)** Take technical and organisational measures to use the system in line with its instructions for use | Policy as code (`policy.yaml`, or chart values) reviewed in Git. The version in force is recorded with every decision |
| **26(2)** Assign human oversight to people with the necessary competence, training, authority and support | Who may approve is an RBAC binding (`approvers` in the chart). The guard records the authenticated identity of every decider. **Competence and training are yours** |
| **26(5)** Monitor operation. If use may present a risk, inform the provider and the market surveillance authority, and suspend use. Report serious incidents | Guardian alerts for monitoring; the stop above for suspending; the evidence export as the record to share. **Informing providers and authorities, and incident reporting (Art. 73), are yours** |
| **26(6)** Keep the logs the system generates automatically, to the extent they are under your control, for a period appropriate to the purpose, of at least six months, unless other law says otherwise. Financial institutions keep them as part of their documentation under financial-services law | As for Art. 19(1): records the guard never deletes, plus archived, verifiable exports |

## Article 72: post-market monitoring (providers)

| Obligation (paraphrased) | Sentinel Guard |
|---|---|
| **72(1)–(2)** Providers run a documented post-market monitoring system. It actively collects and analyses performance data over the system's lifetime, including from deployers, to evaluate continued compliance | Each bundle's oversight summary is that data for the action layer, per agent and recomputable: proposed, blocked, auto-approved, approved, denied, executed, expired; expiries nobody decided; decision times. The metrics give the same live |

Article 72 sits in Chapter IX, not Chapter III. Check with counsel when it applies to your system.

## What remains yours

- **Classification.** Is the system high-risk, and are you its provider, its deployer, or both?
- **People:** their competence, training and authority to oversee (26(2)).
- **Reporting:** to providers and authorities, serious incidents (26(5), 73), the post-market monitoring plan (72(3)).
- **Retention:** the period itself, and reconciling it with data-protection law. Requests can contain personal data in targets, intents or notes.
- **Coverage:** the guard only sees actions routed through it. In Kubernetes, the webhook covers every agent identity you list. Elsewhere, coverage depends on agents' credentials only working through an executor that redeems capabilities.

Sources: the article pages at [artificialintelligenceact.eu](https://artificialintelligenceact.eu/article/12/) (an unofficial reproduction of the Official Journal text, consulted 24 October 2026) and the [Digital Omnibus summary](https://artificialintelligenceact.eu/ai-act-explorer/digital-omnibus/). Check against [EUR-Lex](https://eur-lex.europa.eu/eli/reg/2024/1689/oj) before relying on them.
