# ADR-0007: Deployment on Kubernetes (Helm) and AWS (Terraform)

**Status:** accepted, 2026-10-05

## Decision
- **One Helm chart, three overlays:** defaults (production-shaped), `values-kind.yaml` (local cluster, NodePorts, in-cluster dev dependencies), `values-aws.yaml` (EKS with managed data stores, TLS everywhere, secrets from Secrets Manager).
- **Data stores are not in the chart.**
  - Production: RDS Postgres 16 (pgvector), MSK, ElastiCache, provisioned by `deploy/terraform/aws`.
  - Local: a separate dev-only `sentinel-deps` chart.
  - Stateful systems have different lifecycles and owners than stateless services.
- **Migrations as a Helm hook Job** (`pre-install,pre-upgrade`): schema changes land before new pods roll out. The Job is the only workload with admin DB credentials; serving pods use the RLS-restricted app role with Flyway disabled.
- **Default-deny NetworkPolicy plus an explicit allow-list:** console → gateway → agent → MCP servers → observed services, and agent → gateway `/mcp`. Verified on kind: the console pod can reach the gateway but not the agent or MCP server. On EKS, the VPC CNI's network-policy agent enforces it (enabled in Terraform).
- **Hardened pods:** non-root (uid 10001), read-only root filesystem (writable `/tmp` emptyDir only), all capabilities dropped, seccomp `RuntimeDefault`, requests/limits, startup/readiness/liveness probes, topology spread, PDBs, HPAs on the gateway and agent.
- **Immutable images:**
  - The embedding model is baked in (no runtime download).
  - Java images package a jar built once in CI.
  - Python images run their virtualenv directly, so no package manager is needed at runtime.
  - ECR tags are immutable and scanned on push.
- **Terraform without community modules:** plain resources (VPC, EKS, RDS, MSK, ElastiCache, ECR, KMS, Secrets Manager). Explicit, reviewable, and no dependency on registry downloads.

## What running it on a real cluster caught
The hardened setup surfaced three bugs that compose never would have:
1. **`uv run` needs a writable cache.** The read-only root filesystem broke both Python services; the fix is to run the venv directly.
2. **An empty `OLLAMA_BASE_URL` from the ConfigMap** overrode Spring AI's default and crashed the gateway even with Claude selected. Optional settings are now omitted when empty.
3. **Default-deny blocked NodePort traffic.** Policies were enforced (kind's CNI supports them), which is what we want. External access is now an explicit per-component allow (`networkPolicy.externalAccess`).

## Not done (yet)
- `terraform plan/apply` needs an AWS account. The configuration is validated (`terraform validate`, `fmt`), not applied.
- External Secrets Operator, the AWS Load Balancer Controller and an OTel Collector are assumed to be installed cluster add-ons.
- Java images are ~1.2 GB (ONNX runtime natives for all platforms plus the model). Trimming to one platform's natives and using jlink would roughly halve that.
