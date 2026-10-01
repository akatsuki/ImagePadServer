"""Print profile-separated summaries from CPU20% run manifests."""

import argparse
import json
from pathlib import Path

from budget_bench import summarize_runs


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--run-root", type=Path, required=True)
    args = parser.parse_args()
    print(json.dumps(summarize_runs(args.run_root), ensure_ascii=False, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
