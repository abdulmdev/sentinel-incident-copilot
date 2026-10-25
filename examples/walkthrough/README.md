# Walkthrough: Sentinel Guard in about a minute

[![Open in GitHub Codespaces](https://github.com/codespaces/badge.svg)](https://codespaces.new/abdulmdev/sentinel-incident-copilot?quickstart=1)

```bash
scripts/guard-demo.sh
```

The script builds the guard and starts it on your machine, with [this policy](policy.yaml) (no Docker needed). A refund agent then uses the [Python SDK](../../sdk/python) ([`walkthrough.py`](walkthrough.py)):

1. It **asks** before refunding €42. The guard holds the request for a person.
2. It tries to **approve its own request**: refused.
3. It tries to act with a **made-up capability**: refused (only the guard can sign one).
4. **Alice approves** that refund: that order, that amount.
5. The agent gets a **single-use capability**; the payments service redeems it, and the refund runs once.
6. Someone **replays** the capability: refused.
7. A **bigger refund** is a new request. **Bob denies** it.
8. **Deleting customer data** is blocked by policy outright. **Sending a receipt** runs without a person (low risk), and is still recorded.
9. The record is **exported** as one evidence file and **verified offline**: every signature, the chain, the policy version, plus a summary of who decided what.
10. Someone **edits** the file to say Mallory approved the refund. Verification **fails and names the record**.
11. The **metrics** behind the guardian alerts: per-agent outcomes, open requests, the chain's health.

Every step checks its own result, and the script exits non-zero if anything didn't behave as described (CI runs it).

## On Kubernetes

```bash
scripts/guard-demo.sh kubernetes
```

This needs Docker; the Codespace has it. It creates a kind cluster, installs the guard with its Helm chart, and runs the end-to-end suite:
- an agent's `kubectl scale` waits for `kubectl sentinel approve`;
- it is admitted once;
- scale-to-zero is blocked;
- records can't be edited or deleted;
- the guard going down stops only agents;
- the evidence export verifies offline.

The cluster is kept afterwards, so you can try `kubectl sentinel requests --all` and `kubectl sentinel audit export`.

Next: [governance and compliance](../../docs/compliance/README.md), and the [guard's user guide](../../docs/guard/README.md).
