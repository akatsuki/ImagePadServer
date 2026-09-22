"""Create a deterministic high-density T11 snapshot without changing the source snapshot."""

from __future__ import annotations

import argparse
import copy
import json
from pathlib import Path
from typing import Any


def build_high_density_snapshot(source: dict[str, Any], *, duration_ms: int = 10000, multiplier: int = 4) -> dict[str, Any]:
    if duration_ms <= 0 or multiplier <= 0:
        raise ValueError("duration_ms and multiplier must be positive")
    result = copy.deepcopy(source)
    for thread in result.get("threads", []):
        comments = thread.get("comments", [])
        expanded: list[dict[str, Any]] = []
        for index, comment in enumerate(comments):
            for copy_index in range(multiplier):
                item = copy.deepcopy(comment)
                item["id"] = f"t11-hd-{index:06d}-{copy_index}"
                item["no"] = (index * multiplier) + copy_index + 1
                item["vposMs"] = (index * multiplier + copy_index) * 17 % duration_ms
                expanded.append(item)
        thread["comments"] = expanded
        thread["commentCount"] = len(expanded)
    result["videoId"] = f"{result.get('videoId', 'snapshot')}-t11-hd"
    return result


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--duration-ms", type=int, default=10000)
    parser.add_argument("--multiplier", type=int, default=4)
    args = parser.parse_args(argv)
    source = json.loads(args.source.read_text(encoding="utf-8"))
    result = build_high_density_snapshot(source, duration_ms=args.duration_ms, multiplier=args.multiplier)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(json.dumps({"output": str(args.output), "duration_ms": args.duration_ms, "multiplier": args.multiplier, "threads": [len(t.get("comments", [])) for t in result.get("threads", [])]}, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
