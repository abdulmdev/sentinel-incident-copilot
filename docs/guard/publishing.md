# Publishing Sentinel Guard

A `v2.*` tag runs [`release-guard.yml`](../../.github/workflows/release-guard.yml). With no setup at all, it publishes:

| What | Where |
|---|---|
| The guard image: linux/amd64 and arm64, SBOM, provenance, signed with cosign (keyless) | `ghcr.io/abdulmdev/sentinel-guard:<version>` |
| The Helm chart, signed with cosign (keyless) | `oci://ghcr.io/abdulmdev/charts/sentinel-guard --version <version>` |
| `kubectl-sentinel` for 5 platforms, checksums, the krew manifest, `install.yaml` | the GitHub release |
| The Python SDK (wheel and sdist) and the TypeScript SDK (npm tarball) | the GitHub release |

Three more channels are off until the repository opts in. Each needs a one-time step on the registry's side, under the maintainer's own account. The workflow never holds a long-lived token: PyPI and npm trust its GitHub OIDC identity ("trusted publishing").

## PyPI: `pip install sentinel-guard`

1. On [pypi.org](https://pypi.org), go to *Your projects → Publishing → Add a new pending publisher*, and fill it in:
   - project `sentinel-guard`;
   - owner `abdulmdev`, repository `sentinel-incident-copilot`;
   - workflow `release-guard.yml`;
   - environment `pypi`.
2. In the GitHub repository, go to *Settings → Environments* and create `pypi`. Optionally, require a reviewer.
3. Set the repository variable `PUBLISH_PYPI=true` (*Settings → Secrets and variables → Actions → Variables*).

The next tag publishes. The version comes from the tag, in PEP 440 form: `v2.0.0-alpha.3` is published as `2.0.0a3`, which pip installs only with `--pre`.

## npm: `npm install @sentinel-guard/sdk`

1. Create the npm organization `sentinel-guard`, which owns the `@sentinel-guard` scope. Or rename the package in `sdk/typescript/package.json` to a scope you own.
2. Publish the first version by hand from the release's tarball: `npm publish sentinel-guard-sdk-<version>.tgz --access public`. Trusted publishing is configured on an existing package.
3. In the package's settings on npmjs.com, add a *trusted publisher*: GitHub Actions, `abdulmdev/sentinel-incident-copilot`, workflow `release-guard.yml`, environment `npm`.
4. Create the GitHub environment `npm`, and set `PUBLISH_NPM=true`.

Prereleases are published under the `next` dist-tag, so `npm install @sentinel-guard/sdk` keeps resolving to the latest stable version. npm attaches provenance automatically with trusted publishing.

## Artifact Hub: the chart in the Kubernetes package index

1. Sign in at [artifacthub.io](https://artifacthub.io), then add a repository:
   - kind *Helm charts*;
   - URL `oci://ghcr.io/abdulmdev/charts/sentinel-guard`.
2. Copy the repository ID it shows, and set it as the variable `ARTIFACTHUB_REPOSITORY_ID`.

Releases then also push `artifacthub-repo.yml` (the `artifacthub.io` tag next to the chart), which earns the *verified publisher* badge. The chart's `Chart.yaml` already carries Artifact Hub's annotations: license, category, CRDs, links and images. The release workflow sets the image version and the prerelease flag from the tag.

## Checking a release

```bash
cosign verify ghcr.io/abdulmdev/sentinel-guard:<version> \
  --certificate-identity-regexp 'https://github.com/abdulmdev/sentinel-incident-copilot/.github/workflows/release-guard.yml@.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
cosign verify ghcr.io/abdulmdev/charts/sentinel-guard:<version> \
  --certificate-identity-regexp 'https://github.com/abdulmdev/sentinel-incident-copilot/.github/workflows/release-guard.yml@.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```
