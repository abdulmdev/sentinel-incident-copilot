"""RCA benchmark runner: drives the real agent graph over the 100 replayed incidents.

  uv run --project agent-service python evals/rca/run.py                    # baseline (no API key needed)
  uv run --project agent-service python evals/rca/run.py --check            # + CI gate vs thresholds.json
  uv run --project agent-service python evals/rca/run.py --mode llm --max-usd 15   # Claude (needs ANTHROPIC_API_KEY)
  uv run --project agent-service python evals/rca/run.py --mode llm --provider ollama   # local model, free

Writes results/<run>.json (run = scripted | claude | ollama) and REPORT.md comparing every run so far.
"""
from __future__ import annotations

import argparse
import asyncio
import json
import os
import pathlib
import sys
import time

HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parents[1]
sys.path.insert(0, str(ROOT / "agent-service"))
sys.path.insert(0, str(HERE))

from langgraph.checkpoint.memory import InMemorySaver  # noqa: E402

from app import graph as agent_graph  # noqa: E402
from app import llm as agent_llm  # noqa: E402
from replay import ReplayToolBox, RunbookIndex  # noqa: E402
from scoring import breakdown, score, summarize  # noqa: E402

GATED = {  # metric -> direction; thresholds.json holds the numbers per mode
    "action_accuracy": "min", "cause_accuracy": "min", "triage_accuracy": "min",
    "citation_faithfulness": "min", "grounded_rate": "min", "runbook_citation_precision": "min",
    "escalation_recall": "min", "harmful_action_rate": "max", "avg_tool_calls": "max",
    "attack_success_rate": "max", "pii_leak_rate": "max",
}


RUNS = ("scripted", "claude", "ollama")


def load(limit: int | None, family: str | None, name: str = "scenarios.jsonl", sample: int | None = None) -> list[dict]:
    rows = [json.loads(l) for l in (HERE / name).read_text().splitlines() if l]
    rows = [r for r in rows if family is None or r["family"] == family]
    if sample:
        # Round-robin over (family, tier) groups so every combination is represented before any repeats.
        groups: dict[tuple, list] = {}
        for r in rows:
            groups.setdefault((r["family"], r["difficulty"]), []).append(r)
        picked, i = [], 0
        while len(picked) < min(sample, len(rows)):
            for g in groups.values():
                if i < len(g) and len(picked) < sample:
                    picked.append(g[i])
            i += 1
        rows = picked
    return rows[:limit] if limit else rows


class Progress:
    """One line per finished scenario with a running ETA; long LLM runs must never look stuck."""

    def __init__(self, total: int):
        self.total, self.done, self.correct, self.started = total, 0, 0, time.perf_counter()

    def tick(self, result: dict) -> None:
        self.done += 1
        self.correct += result["score"]["action_correct"]
        elapsed = time.perf_counter() - self.started
        eta = elapsed / self.done * (self.total - self.done)
        mark = "ok  " if result["score"]["action_correct"] else ("HARM" if result["score"]["harmful"] else "miss")
        print(f"[{self.done:3}/{self.total}] {mark} {result['id']:32} {result['latency_ms'] / 1000:6.1f}s  "
              f"acc {self.correct / self.done:5.1%}  eta {eta / 60:4.1f} min", flush=True)


async def run_one(scenario: dict, index: RunbookIndex, sem: asyncio.Semaphore,
                  progress: Progress | None = None) -> dict:
    async with sem:
        toolbox = ReplayToolBox(scenario, index)
        graph = agent_graph.build_graph(toolbox, InMemorySaver())
        incident = {"id": scenario["id"], "tenant_id": "eval", **scenario["incident"]}
        cfg = agent_graph.run_config("eval", scenario["id"], tool_token="eval-token")
        started = time.perf_counter()
        try:
            await graph.ainvoke({"incident": incident}, cfg)
            state = (await graph.aget_state(cfg)).values
            error = None
        except Exception as e:  # a crash is a scored failure, not a harness abort
            state, error = {}, f"{type(e).__name__}: {e}"
        latency_ms = round((time.perf_counter() - started) * 1000)
        result = {"id": scenario["id"], "family": scenario["family"], "difficulty": scenario["difficulty"],
                  "expected": scenario["expected"], "score": score(scenario, state), "latency_ms": latency_ms,
                  "tool_calls": toolbox.calls, "error": error}
        if progress:
            progress.tick(result)
        return result


def table(rows: list[list], header: list[str]) -> str:
    out = ["| " + " | ".join(header) + " |", "|" + "---|" * len(header)]
    out += ["| " + " | ".join("" if c is None else str(c) for c in r) + " |" for r in rows]
    return "\n".join(out)


def pct(x):
    return "—" if x is None else f"{x:.0%}"


def write_report() -> None:
    runs = {m: json.loads((HERE / "results" / f"{m}.json").read_text())
            for m in RUNS if (HERE / "results" / f"{m}.json").exists()}
    modes = [m for m in RUNS if m in runs]
    label = {"scripted": "Baseline (scripted + rules)", "claude": "Claude (LLM investigator)"}
    if "ollama" in runs:
        label["ollama"] = f"Local LLM ({runs['ollama'].get('model', 'ollama')})"
    # An LLM run on a sample is compared against the baseline on exactly the same incidents.
    sampled = next((runs[m] for m in ("ollama", "claude") if m in runs and runs[m].get("sample")), None)
    if sampled and "scripted" in runs:
        ids = {r["id"] for r in sampled["results"]}
        base = [r for r in runs["scripted"]["results"] if r["id"] in ids]
        runs["scripted_sample"] = {"summary": summarize(base), "results": base}
        modes = ["scripted", "scripted_sample", *[m for m in modes if m != "scripted"]]
        label["scripted_sample"] = f"Baseline on the same {len(ids)}"
        for m in ("ollama", "claude"):
            if m in runs and runs[m].get("sample"):
                label[m] += f" · sample of {len(runs[m]['results'])}"
    metric_rows = [
        ("Remediation accuracy", "action_accuracy", pct), ("Root-cause accuracy", "cause_accuracy", pct),
        ("Triage accuracy", "triage_accuracy", pct), ("**Harmful action rate** (lower is better)",
                                                      "harmful_action_rate", pct),
        ("Harmful before policy interlocks", "harmful_action_rate_unguarded", pct),
        ("Prompt-injection attacks that succeeded", "attack_success_rate", pct),
        ("...before policy interlocks", "attack_success_rate_unguarded", pct),
        ("PII reaching model or evidence", "pii_leak_rate", pct),
        ("Escalates when it should", "escalation_recall", pct),
        ("Citation faithfulness (no made-up sources)", "citation_faithfulness", pct),
        ("Lead cause grounded in telemetry", "grounded_rate", pct),
        ("Cites a runbook", "runbook_citation_rate", pct),
        ("Runbook citation precision", "runbook_citation_precision", pct),
        ("Avg tool calls", "avg_tool_calls", str), ("Total LLM cost", "total_cost_usd", lambda x: f"${x:.2f}"),
    ]
    metric_rows.append(("p50 latency per incident", "p50_latency_ms", lambda x: f"{x / 1000:.1f}s"))
    cols = [label[m] for m in modes] + ([] if "claude" in runs else ["Claude (LLM investigator)"])
    rows = []
    for name, key, fmt in metric_rows:
        row = [name] + [fmt(runs[m]["summary"][key]) for m in modes]
        if "claude" not in runs:
            row.append("not run")
        rows.append(row)

    parts = [
        "# RCA benchmark report", "",
        "Generated by `evals/rca/run.py`. Do not edit by hand.", "",
        f"{runs[modes[0]]['summary']['n']} replayed incidents across 8 failure families "
        "and 6 difficulty tiers including prompt injection "
        "(`scenarios.jsonl`, generated by `generate.py`). Metrics are deterministic checks; there is no LLM judge.", "",
        table(rows, ["Metric", *cols]), "",
    ]
    if "claude" not in runs:
        parts += ["> The Claude column needs `ANTHROPIC_API_KEY`: "
                  "`uv run --project agent-service python evals/rca/run.py --mode llm --max-usd 15`.", ""]
    history_file = HERE / "results" / "history.jsonl"
    if history_file.exists():
        hist = [json.loads(l) for l in history_file.read_text().splitlines() if l]
        parts += ["## History", "",
                  table([[h["date"], h["label"], h["scenarios"], pct(h["summary"]["action_accuracy"]),
                          pct(h["summary"]["harmful_action_rate"]), pct(h["summary"]["cause_accuracy"]),
                          pct(h["summary"]["triage_accuracy"])] for h in hist],
                        ["Date", "Run", "Set", "Remediation", "Harmful", "Root cause", "Triage"]), ""]
    for m in [m for m in modes if m != "scripted_sample"]:
        res = runs[m]
        parts += [f"## {label[m]}: by difficulty", "",
                  table([[k, v["n"], pct(v["action_accuracy"]), pct(v["harmful_action_rate"])]
                         for k, v in res["by_difficulty"].items()],
                        ["Tier", "n", "Remediation accuracy", "Harmful"]), "",
                  f"## {label[m]}: by failure family", "",
                  table([[k, v["n"], pct(v["action_accuracy"]), pct(v["harmful_action_rate"])]
                         for k, v in res["by_family"].items()],
                        ["Family", "n", "Remediation accuracy", "Harmful"]), ""]
        misses = [r for r in res["results"] if not r["score"]["action_correct"]]
        if misses:
            parts += [f"### {label[m]}: misses ({len(misses)})", "",
                      table([[r["id"], " or ".join(a["action"] + (f"→{a['target']}" if a["target"] else "")
                                                   for a in r["expected"]["actions"]),
                              r["score"]["action"], r["score"]["top_cause"], "yes" if r["score"]["harmful"] else ""]
                             for r in misses],
                            ["Scenario", "Expected", "Proposed", "Top cause", "Harmful"]), ""]
    (HERE / "REPORT.md").write_text("\n".join(parts))


def check(summary: dict, mode: str) -> list[str]:
    thresholds = json.loads((HERE / "thresholds.json").read_text()).get(mode, {})
    failures = []
    for key, bound in thresholds.items():
        if key.startswith("_"):
            continue
        value = summary[key]
        if GATED[key] == "min" and value < bound:
            failures.append(f"{key} = {value} < {bound}")
        if GATED[key] == "max" and value > bound:
            failures.append(f"{key} = {value} > {bound}")
    return failures


async def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--mode", choices=["scripted", "llm"], default="scripted")
    ap.add_argument("--provider", choices=["anthropic", "ollama"], default="anthropic",
                    help="for --mode llm: Claude (needs ANTHROPIC_API_KEY) or a local Ollama model")
    ap.add_argument("--limit", type=int)
    ap.add_argument("--family")
    ap.add_argument("--concurrency", type=int, default=8)
    ap.add_argument("--max-usd", type=float, help="required for --mode llm: abort if the worst case exceeds it")
    ap.add_argument("--check", action="store_true", help="exit 1 if a metric crosses thresholds.json")
    ap.add_argument("--label", help="record this full run in results/history.jsonl under this label")
    ap.add_argument("--scenarios", default="scenarios.jsonl", help="scenario file in evals/rca/")
    ap.add_argument("--sample", type=int, help="stratified sample: N scenarios spread across every family x tier")
    ap.add_argument("--resume", action="store_true",
                    help="skip scenarios already in results/<run>.partial.jsonl (results are saved as they finish)")
    args = ap.parse_args()

    scenarios = load(args.limit, args.family, args.scenarios, args.sample)
    reference = args.scenarios == "scenarios.jsonl" and not args.limit and not args.family and not args.sample
    run_name = "scripted" if args.mode == "scripted" else ("ollama" if args.provider == "ollama" else "claude")
    if args.mode == "scripted":
        os.environ.pop("ANTHROPIC_API_KEY", None)  # baseline must never call a model, even if a key is set
        os.environ.pop("SENTINEL_LLM_PROVIDER", None)
    elif args.provider == "ollama":
        os.environ["SENTINEL_LLM_PROVIDER"] = "ollama"
        args.concurrency = min(args.concurrency, 2)  # one local GPU: more parallelism only queues
    else:
        os.environ["SENTINEL_LLM_PROVIDER"] = "anthropic"
        if not os.getenv("ANTHROPIC_API_KEY"):
            print("--mode llm needs ANTHROPIC_API_KEY. The baseline runs without it: --mode scripted.")
            return 2
        worst = len(scenarios) * (agent_graph.MAX_COST_USD + 0.25)  # investigation cap + triage/RCA allowance
        if args.max_usd is None or worst > args.max_usd:
            print(f"Worst-case spend for {len(scenarios)} scenarios is ${worst:.2f}. "
                  f"Re-run with --max-usd {worst:.0f} (or --limit) to confirm.")
            return 2
        args.concurrency = min(args.concurrency, 4)  # stay well inside API rate limits

    index = RunbookIndex()
    sem = asyncio.Semaphore(args.concurrency)
    started = time.perf_counter()
    # Results are appended as each scenario finishes, so an interrupted multi-hour LLM run loses nothing.
    (HERE / "results").mkdir(exist_ok=True)
    partial = HERE / "results" / f"{run_name}.partial.jsonl"
    done: list[dict] = []
    if args.resume and partial.exists():
        done = [json.loads(l) for l in partial.read_text().splitlines() if l]
        finished = {r["id"] for r in done}
        scenarios = [s for s in scenarios if s["id"] not in finished]
        print(f"resuming: {len(done)} already done, {len(scenarios)} to go", flush=True)
    elif partial.exists():
        partial.unlink()
    progress = Progress(len(scenarios)) if args.mode == "llm" else None  # baseline takes ~1 s: no need

    async def run_and_save(s):
        r = await run_one(s, index, sem, progress)
        with partial.open("a") as f:
            f.write(json.dumps(r, default=str) + "\n")
        return r
    results = done + list(await asyncio.gather(*(run_and_save(s) for s in scenarios)))
    summary = summarize(results)
    out = {"mode": args.mode, "run": run_name,
           "model": agent_llm.model_for("investigate") if args.mode == "llm" else None,
           "summary": summary, "by_difficulty": breakdown(results, "difficulty"),
           "by_family": breakdown(results, "family"), "results": results}
    (HERE / "results").mkdir(exist_ok=True)
    out["sample"] = args.sample
    if reference or (args.sample and run_name != "scripted"):  # an LLM sample is that run's reference
        (HERE / "results" / f"{run_name}.json").write_text(json.dumps(out, indent=1, default=str))
    partial.unlink(missing_ok=True)
    if args.label:
        entry = {"label": args.label, "mode": run_name, "scenarios": args.scenarios,
                 "date": time.strftime("%Y-%m-%d"), "summary": summary}
        with (HERE / "results" / "history.jsonl").open("a") as f:
            f.write(json.dumps(entry) + "\n")
    if reference or args.label:
        write_report()

    print(f"{run_name}: {len(results)} scenarios in {time.perf_counter() - started:.1f}s")
    for k, v in summary.items():
        print(f"  {k:28} {v}")
    errors = [r for r in results if r["error"]]
    if errors:
        print(f"\n{len(errors)} scenarios crashed, e.g. {errors[0]['id']}: {errors[0]['error']}")
    if args.check:
        failures = check(summary, run_name)
        print("\nGATE: " + ("PASS" if not failures else "FAIL\n  " + "\n  ".join(failures)))
        return 1 if failures or errors else 0
    return 1 if errors else 0


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))
