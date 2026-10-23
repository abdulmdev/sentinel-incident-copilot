"""Writes the krew plugin manifest for a kubectl-sentinel release.

  python3 scripts/krew_manifest.py <version> <checksums.txt> <repo-url> > plugins/sentinel.yaml

checksums.txt is the release's `sha256sum` output for kubectl-sentinel_<os>_<arch>.tar.gz. The same
file goes in this repository's plugins/ directory, which makes the repo a krew index:
  kubectl krew index add sentinel https://github.com/abdulmdev/sentinel-incident-copilot.git
  kubectl krew install sentinel/sentinel
"""
import sys

PLATFORMS = [("linux", "amd64"), ("linux", "arm64"), ("darwin", "amd64"), ("darwin", "arm64"), ("windows", "amd64")]


def main() -> None:
    version, checksums, repo = sys.argv[1], sys.argv[2], sys.argv[3].rstrip("/")
    sums = {}
    for line in open(checksums):
        digest, name = line.split()
        sums[name.lstrip("*")] = digest
    out = [
        "apiVersion: krew.googlecontainertools.github.com/v1alpha2",
        "kind: Plugin",
        "metadata:",
        "  name: sentinel",
        "spec:",
        f"  version: v{version}",
        f"  homepage: {repo}",
        "  shortDescription: Approve AI agent actions guarded by Sentinel Guard",
        "  description: |",
        "    Sentinel Guard stops AI agents from changing your cluster unless policy, or a human,",
        "    approved that exact change, and then admits it once. This plugin lists pending agent",
        "    actions, approves or denies them with your own Kubernetes credentials, and verifies",
        "    the guard's signed, hash-chained audit trail.",
        "  platforms:",
    ]
    for os_, arch in PLATFORMS:
        name = f"kubectl-sentinel_{os_}_{arch}.tar.gz"
        if name not in sums:
            sys.exit(f"missing checksum for {name}")
        binary = "kubectl-sentinel.exe" if os_ == "windows" else "kubectl-sentinel"
        out += [
            "    - selector:",
            "        matchLabels:",
            f"          os: {os_}",
            f"          arch: {arch}",
            f"      uri: {repo}/releases/download/v{version}/{name}",
            f"      sha256: {sums[name]}",
            "      files:",
            f"        - from: {binary}",
            "          to: .",
            "        - from: LICENSE",
            "          to: .",
            f"      bin: {binary}",
        ]
    print("\n".join(out))


if __name__ == "__main__":
    main()
