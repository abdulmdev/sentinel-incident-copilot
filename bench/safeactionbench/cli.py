"""safeactionbench: describe | serve | score | baseline | import-sentinel | leaderboard."""
from __future__ import annotations

import argparse
import json
import sys
from collections import Counter
from pathlib import Path

from . import baselines, dataset
from .leaderboard import load_entries, markdown, page
from .scoring import score

KEYS = ["accuracy", "harmful_rate_proposed", "harmful_rate", "harmful_rate_guarded", "fix_rate", "fix_rate_guarded",
        "escalation_recall", "escalation_recall_guarded", "attack_success_rate_proposed", "attack_success_rate",
        "attack_success_rate_guarded",
        "guard_blocks", "guard_false_blocks", "answered", "n"]


def read_predictions(path: str) -> list[dict]:
    text = sys.stdin.read() if path == "-" else Path(path).read_text()
    text = text.strip()
    if text.startswith("["):
        return json.loads(text)
    return [json.loads(line) for line in text.splitlines() if line.strip()]


def from_sentinel(path: str) -> list[dict]:
    """Predictions from a Sentinel evals/rca results file: the proposal before and after its own checks."""
    data = json.loads(Path(path).read_text())
    return [{"id": r["id"], "action": r["score"]["action"], "target": r["score"]["target"],
             "proposed_action": r["score"].get("raw_action", r["score"]["action"])} for r in data["results"]]


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(prog="safeactionbench", description=__doc__)
    sub = ap.add_subparsers(dest="cmd", required=True)
    d = sub.add_parser("describe", help="what's in a split")
    d.add_argument("--split", default="dev", choices=dataset.SPLITS)
    s = sub.add_parser("serve", help="an MCP server (stdio) any agent can take the benchmark through")
    s.add_argument("--split", default="dev", choices=dataset.SPLITS)
    s.add_argument("--out", required=True, help="append proposals to this JSONL file")
    s.add_argument("--limit", type=int)
    sc = sub.add_parser("score", help="score predictions (JSONL or a JSON list; '-' for stdin)")
    sc.add_argument("predictions")
    sc.add_argument("--split", default="dev", choices=dataset.SPLITS)
    sc.add_argument("--json", action="store_true", help="the full result, including per-incident rows")
    b = sub.add_parser("baseline", help="predictions of a built-in baseline")
    b.add_argument("name", choices=sorted(baselines.BASELINES))
    b.add_argument("--split", default="dev", choices=dataset.SPLITS)
    i = sub.add_parser("import-sentinel", help="predictions from a Sentinel evals/rca results file")
    i.add_argument("results")
    lb = sub.add_parser("leaderboard", help="render the leaderboard from stored entries")
    lb.add_argument("--results", default="bench/results")
    lb.add_argument("--html", help="write the HTML page here")
    lb.add_argument("--check", help="exit 1 if this HTML file is not what --results renders")
    args = ap.parse_args(argv)

    if args.cmd == "describe":
        rows = dataset.load(args.split)
        print(json.dumps({"split": args.split, "incidents": len(rows),
                          "families": Counter(r["family"] for r in rows),
                          "difficulty": Counter(r["difficulty"] for r in rows),
                          "escalate_only": sum(dataset.should_escalate(r) for r in rows)}, indent=2))
    elif args.cmd == "serve":
        from .server import Bench, build
        build(Bench(args.split, args.out, args.limit)).run()  # stdio
    elif args.cmd == "score":
        result = score(dataset.load(args.split), read_predictions(args.predictions))
        if args.json:
            print(json.dumps(result, indent=2))
        else:
            for k in KEYS:
                v = result["summary"][k]
                print(f"{k:30s} {v if v is not None else '-'}")
    elif args.cmd == "baseline":
        for p in baselines.run(args.name, args.split):
            print(json.dumps(p))
    elif args.cmd == "import-sentinel":
        for p in from_sentinel(args.results):
            print(json.dumps(p))
    elif args.cmd == "leaderboard":
        entries = load_entries(Path(args.results))
        rendered = page(entries)
        if args.check:
            if Path(args.check).read_text() != rendered:
                print(f"{args.check} is out of date: run safeactionbench leaderboard --html {args.check}")
                return 1
            print("leaderboard up to date")
        elif args.html:
            Path(args.html).write_text(rendered)
            print(f"wrote {args.html} ({len(entries)} entries)")
        else:
            for split in dataset.SPLITS:
                print(f"\n{split}\n{markdown(entries, split)}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
