"""Retrieval eval: recall@k and MRR for vector, keyword and hybrid search.

  python evals/retrieval/run.py                 # prints a markdown table
  python evals/retrieval/run.py --min-recall 0.9  # exits 1 if hybrid recall@5 is below 0.9 (CI gate)

A question counts as a hit at k if any of its labeled relevant documents is in the top k.
Chunks are deduplicated to documents before ranking, because citing three chunks of one
runbook shouldn't count as three results.
"""
import argparse
import json
import pathlib
import subprocess
import sys
import urllib.parse
import urllib.request

ROOT = pathlib.Path(__file__).resolve().parents[2]
GATEWAY = "http://localhost:8080"
MODES = ("vector", "keyword", "hybrid")


def search(token: str, q: str, mode: str, k: int = 20) -> list[str]:
    url = f"{GATEWAY}/api/v1/knowledge/search?" + urllib.parse.urlencode({"q": q, "mode": mode, "k": k})
    req = urllib.request.Request(url, headers={"Authorization": f"Bearer {token}"})
    with urllib.request.urlopen(req) as r:
        docs = [hit["docId"] for hit in json.load(r)]
    return list(dict.fromkeys(docs))  # dedupe chunks → documents, keep rank order


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--tenant", default="acme")
    ap.add_argument("--min-recall", type=float, default=None)
    ap.add_argument("--verbose", action="store_true")
    args = ap.parse_args()

    token = subprocess.check_output([sys.executable, str(ROOT / "scripts/dev_token.py"), "eval@ci", args.tenant],
                                    text=True).strip()
    questions = [json.loads(l) for l in (ROOT / "evals/retrieval/questions.jsonl").read_text().splitlines() if l]

    results = {}
    for mode in MODES:
        r1 = r5 = rr = 0.0
        for item in questions:
            ranked = search(token, item["q"], mode)
            ranks = [i for i, d in enumerate(ranked) if d in item["relevant"]]
            first = ranks[0] + 1 if ranks else None
            r1 += first == 1
            r5 += first is not None and first <= 5
            rr += 1 / first if first else 0
            if args.verbose and first != 1:
                print(f"[{mode}] rank={first} q={item['q']!r} top={ranked[:3]}")
        n = len(questions)
        results[mode] = (r1 / n, r5 / n, rr / n)

    print(f"\n{len(questions)} questions, tenant={args.tenant}\n")
    print("| mode | recall@1 | recall@5 | MRR |\n|---|---|---|---|")
    for mode, (r1, r5, mrr) in results.items():
        print(f"| {mode} | {r1:.2f} | {r5:.2f} | {mrr:.3f} |")

    if args.min_recall is not None and results["hybrid"][1] < args.min_recall:
        print(f"\nFAIL: hybrid recall@5 {results['hybrid'][1]:.2f} < {args.min_recall}")
        sys.exit(1)


if __name__ == "__main__":
    main()
