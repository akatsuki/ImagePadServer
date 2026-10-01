"""Prepare hash-addressed, held-out real-media inputs for T9.2 PGO training."""

from __future__ import annotations

import argparse
import copy
import hashlib
import json
import os
import stat
from datetime import datetime, timezone
from pathlib import Path


COVERAGE = ("short", "long", "dense", "NCT1", "NCT2")
DENSE_WINDOW_MS = 2000


def sha256_file(path: Path) -> str:
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or path.is_symlink():
        raise ValueError(f"input must be a regular non-symlink file: {path}")
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def load_snapshot(path: Path) -> tuple[dict, list[dict]]:
    sha256_file(path)
    snapshot = json.loads(path.read_text(encoding="utf-8-sig"))
    if not isinstance(snapshot, dict) or not isinstance(snapshot.get("threads"), list):
        raise ValueError(f"snapshot must contain a threads array: {path}")
    comments: list[dict] = []
    ids: set[str] = set()
    for thread in snapshot["threads"]:
        if not isinstance(thread, dict) or not isinstance(thread.get("comments"), list):
            raise ValueError(f"snapshot thread must contain a comments array: {path}")
        for comment in thread["comments"]:
            if not isinstance(comment, dict):
                raise ValueError(f"snapshot comment must be an object: {path}")
            comment_id = comment.get("id")
            vpos = comment.get("vposMs")
            if not isinstance(comment_id, str) or not comment_id:
                raise ValueError(f"snapshot comment ID is missing: {path}")
            if comment_id in ids:
                raise ValueError(f"snapshot has duplicate comment IDs: {path}")
            if isinstance(vpos, bool) or not isinstance(vpos, int) or vpos < 0:
                raise ValueError(f"snapshot comment vposMs must be a nonnegative integer: {path}")
            ids.add(comment_id)
            comments.append(comment)
    count = snapshot.get("commentCount")
    if count is not None and (isinstance(count, bool) or not isinstance(count, int) or count != len(comments)):
        raise ValueError(f"snapshot commentCount does not match its thread contents: {path}")
    if not comments:
        raise ValueError(f"snapshot must contain at least one comment: {path}")
    return snapshot, comments


def derive_dense_snapshot(snapshot: dict, *, window_ms: int = DENSE_WINDOW_MS) -> dict:
    if window_ms < 1:
        raise ValueError("dense window must be positive")
    derived = copy.deepcopy(snapshot)
    comments = [comment for thread in derived["threads"] for comment in thread["comments"]]
    if not comments:
        raise ValueError("cannot derive a dense snapshot without comments")
    span = window_ms - 1
    denominator = max(1, len(comments) - 1)
    for index, comment in enumerate(comments):
        # Integer round-to-nearest, with ties upward; preserve the original
        # comment order and every field except vposMs.
        comment["vposMs"] = (2 * index * span + denominator) // (2 * denominator)
    return derived


def comment_ids(snapshot: dict) -> set[str]:
    return {comment["id"] for thread in snapshot["threads"] for comment in thread["comments"]}


def material(material_id: str, coverage: str, source: Path, snapshot: Path, seed: int) -> dict:
    return {
        "id": material_id,
        "coverage": coverage,
        "sourceSha256": sha256_file(source),
        "snapshotSha256": sha256_file(snapshot),
        "seed": seed,
    }


def workload(material_record: dict, source: Path, snapshot: Path, protocol: str, *, dense: bool = False) -> dict:
    return {
        "id": material_record["id"],
        "coverage": material_record["coverage"],
        "sourcePath": str(source.resolve()),
        "snapshotPath": str(snapshot.resolve()),
        "protocol": protocol,
        "width": 1920 if dense or material_record["coverage"] in ("NCT1", "NCT2") else 1280,
        "height": 1080 if dense or material_record["coverage"] in ("NCT1", "NCT2") else 720,
        "durationMs": 154955 if material_record["coverage"] == "long" and material_record["id"].startswith("train-") else 60000 if material_record["coverage"] == "long" else 6000,
        "fpsNum": 30000 if material_record["id"].startswith("train-") or material_record["coverage"] != "long" else 30,
        "fpsDen": 1001 if material_record["id"].startswith("train-") or material_record["coverage"] != "long" else 1,
        "backend": "auto",
        "readbackSlots": 3,
        "assetLayout": "separate",
    }


def build_documents(
    *,
    training_short_source: Path,
    training_long_source: Path,
    training_snapshot: Path,
    training_dense_snapshot: Path,
    evaluation_long_source: Path,
    evaluation_snapshot: Path,
    browser_path: Path,
    output_dir: Path,
) -> dict:
    paths = {
        name: path.resolve()
        for name, path in {
            "training_short_source": training_short_source,
            "training_long_source": training_long_source,
            "training_snapshot": training_snapshot,
            "training_dense_snapshot": training_dense_snapshot,
            "evaluation_long_source": evaluation_long_source,
            "evaluation_snapshot": evaluation_snapshot,
            "browser_path": browser_path,
        }.items()
    }
    for name, path in paths.items():
        sha256_file(path)
    temp_root = Path(os.environ.get("TEMP", os.environ.get("TMP", ""))).resolve()
    output_dir = output_dir.absolute()
    if not temp_root.is_absolute() or not output_dir.resolve(strict=False).is_relative_to(temp_root):
        raise ValueError("output directory must be a strict child of the current user TEMP directory")
    if output_dir.exists():
        raise FileExistsError(f"output directory must be new: {output_dir}")
    if not output_dir.parent.is_dir():
        raise ValueError(f"output directory parent must already exist: {output_dir.parent}")
    if output_dir.parent.resolve() != output_dir.parent:
        raise ValueError("output directory parent must not traverse a reparse point")
    output_dir.mkdir(parents=False, exist_ok=False)
    owner_path = output_dir / "pgo-input-owner.json"
    owner_path.write_text(
        json.dumps(
            {
                "schemaVersion": 1,
                "kind": "nico-pgo-input-preparation",
                "outputRoot": str(output_dir.resolve()),
                "status": "preparing",
                "createdUtc": datetime.now(timezone.utc).isoformat(),
            },
            indent=2,
        )
        + "\n",
        encoding="utf-8",
    )

    train_snapshot, _ = load_snapshot(paths["training_snapshot"])
    eval_snapshot, _ = load_snapshot(paths["evaluation_snapshot"])
    dense_train_snapshot, _ = load_snapshot(paths["training_dense_snapshot"])
    train_ids = comment_ids(train_snapshot)
    eval_ids = comment_ids(eval_snapshot)
    if train_ids & eval_ids:
        raise ValueError(f"training and evaluation snapshots share {len(train_ids & eval_ids)} comment IDs")

    derived_eval_dense = derive_dense_snapshot(eval_snapshot)
    dense_eval_path = output_dir / "heldout-dense-snapshot.json"
    dense_eval_path.write_text(json.dumps(derived_eval_dense, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")

    training = {"schemaVersion": 1, "role": "training", "materials": []}
    evaluation = {"schemaVersion": 1, "role": "evaluation", "materials": []}
    train_materials = {}
    eval_materials = {}
    training_inputs = (
        ("short", paths["training_short_source"], paths["training_snapshot"], "NCT1"),
        ("long", paths["training_long_source"], paths["training_snapshot"], "NCT2"),
        ("dense", paths["training_short_source"], paths["training_dense_snapshot"], "NCT2"),
        ("NCT1", paths["training_short_source"], paths["training_snapshot"], "NCT1"),
        ("NCT2", paths["training_short_source"], paths["training_snapshot"], "NCT2"),
    )
    evaluation_inputs = (
        ("short", paths["training_short_source"], paths["evaluation_snapshot"], "NCT1"),
        ("long", paths["evaluation_long_source"], paths["evaluation_snapshot"], "NCT2"),
        ("dense", paths["training_short_source"], dense_eval_path.resolve(), "NCT2"),
        ("NCT1", paths["training_short_source"], paths["evaluation_snapshot"], "NCT1"),
        ("NCT2", paths["training_short_source"], paths["evaluation_snapshot"], "NCT2"),
    )
    for index, (coverage, source, snapshot, protocol) in enumerate(training_inputs):
        record = material(f"train-{coverage.lower()}", coverage, source, snapshot, 7101 + index)
        training["materials"].append(record)
        train_materials[coverage] = workload(record, source, snapshot, protocol, dense=coverage == "dense")
    for index, (coverage, source, snapshot, protocol) in enumerate(evaluation_inputs):
        record = material(f"eval-{coverage.lower()}", coverage, source, snapshot, 8201 + index)
        evaluation["materials"].append(record)
        eval_materials[coverage] = workload(record, source, snapshot, protocol, dense=coverage == "dense")

    train_pairs = {(item["sourceSha256"], item["snapshotSha256"]) for item in training["materials"]}
    eval_pairs = {(item["sourceSha256"], item["snapshotSha256"]) for item in evaluation["materials"]}
    if train_pairs & eval_pairs:
        raise ValueError("training and evaluation contain an overlapping source/snapshot SHA pair")
    if comment_ids(derived_eval_dense) != eval_ids:
        raise ValueError("dense held-out derivation changed evaluation comment IDs")
    if any(not 0 <= comment["vposMs"] < DENSE_WINDOW_MS for thread in derived_eval_dense["threads"] for comment in thread["comments"]):
        raise ValueError("dense held-out derivation placed a comment outside its declared window")

    catalog = {
        "schemaVersion": 1,
        "browserPath": str(paths["browser_path"]),
        "training": [train_materials[coverage] for coverage in COVERAGE],
        "evaluation": [eval_materials[coverage] for coverage in COVERAGE],
    }
    documents = {
        "training.json": training,
        "evaluation.json": evaluation,
        "workload-catalog.json": catalog,
    }
    for name, value in documents.items():
        (output_dir / name).write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")

    preparation = {
        "schemaVersion": 1,
        "kind": "nico-pgo-input-preparation",
        "outputRoot": str(output_dir.resolve()),
        "createdUtc": datetime.now(timezone.utc).isoformat(),
        "inputs": {name: {"path": str(path), "sha256": sha256_file(path)} for name, path in paths.items()},
        "outputs": {name: sha256_file(output_dir / name) for name in (*documents, "heldout-dense-snapshot.json")},
        "trainingCommentCount": len(train_ids),
        "evaluationCommentCount": len(eval_ids),
        "sharedCommentIDCount": 0,
        "denseEvaluationDerivation": {
            "method": "preserve every evaluation comment and field except vposMs; evenly re-clock original order into [0, 1999] ms",
            "windowMs": DENSE_WINDOW_MS,
            "sourceSnapshotSha256": sha256_file(paths["evaluation_snapshot"]),
            "derivedSnapshotSha256": sha256_file(dense_eval_path),
            "commentCount": len(eval_ids),
        },
        "encodedMediaWritten": False,
        "rawRGBAWritten": False,
    }
    preparation["status"] = "complete"
    owner_path.write_text(json.dumps(preparation, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    return preparation


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    for argument in (
        "training-short-source",
        "training-long-source",
        "training-snapshot",
        "training-dense-snapshot",
        "evaluation-long-source",
        "evaluation-snapshot",
        "browser-path",
        "output-dir",
    ):
        parser.add_argument("--" + argument, type=Path, required=True)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    result = build_documents(
        training_short_source=args.training_short_source,
        training_long_source=args.training_long_source,
        training_snapshot=args.training_snapshot,
        training_dense_snapshot=args.training_dense_snapshot,
        evaluation_long_source=args.evaluation_long_source,
        evaluation_snapshot=args.evaluation_snapshot,
        browser_path=args.browser_path,
        output_dir=args.output_dir,
    )
    print(json.dumps({key: value for key, value in result.items() if key not in ("inputs", "outputs")}, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
