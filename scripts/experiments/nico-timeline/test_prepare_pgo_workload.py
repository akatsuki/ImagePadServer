import importlib.util
import json
from pathlib import Path

import pytest


SCRIPT = Path(__file__).with_name("prepare_pgo_workload.py")
SPEC = importlib.util.spec_from_file_location("prepare_pgo_workload", SCRIPT)
MODULE = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(MODULE)


def write_snapshot(path: Path, ids, *, video_id="sm9"):
    comments = [
        {
            "id": comment_id,
            "no": index + 1,
            "vposMs": index * 1000,
            "body": f"private body {comment_id}",
            "commands": ["ue", "red"],
            "userId": f"user-{comment_id}",
        }
        for index, comment_id in enumerate(ids)
    ]
    value = {
        "schemaVersion": 1,
        "videoId": video_id,
        "commentStatus": "ready",
        "commentCount": len(comments),
        "threads": [{"id": "main", "fork": "main", "comments": comments}],
    }
    path.write_text(json.dumps(value, ensure_ascii=False), encoding="utf-8")
    return value


def fixtures(tmp_path):
    source_short = tmp_path / "short.mp4"
    source_long = tmp_path / "long.mp4"
    source_eval_long = tmp_path / "eval-long.mp4"
    browser = tmp_path / "chrome.exe"
    for path, data in (
        (source_short, b"short-video"),
        (source_long, b"long-video"),
        (source_eval_long, b"held-out-video"),
        (browser, b"browser"),
    ):
        path.write_bytes(data)
    train_snapshot = tmp_path / "train.json"
    train_dense = tmp_path / "train-dense.json"
    eval_snapshot = tmp_path / "eval.json"
    write_snapshot(train_snapshot, ["train-a", "train-b", "train-c", "train-d"])
    write_snapshot(train_dense, ["train-a", "train-b", "train-c", "train-d"])
    evaluation = write_snapshot(eval_snapshot, ["eval-a", "eval-b", "eval-c"])
    return {
        "training_short_source": source_short,
        "training_long_source": source_long,
        "training_snapshot": train_snapshot,
        "training_dense_snapshot": train_dense,
        "evaluation_long_source": source_eval_long,
        "evaluation_snapshot": eval_snapshot,
        "browser_path": browser,
        "evaluation": evaluation,
        "output_dir": tmp_path / "prepared",
    }


def call_build(options):
    arguments = {key: value for key, value in options.items() if key not in ("evaluation", "output_dir")}
    return MODULE.build_documents(**arguments, output_dir=options["output_dir"])


def test_preparation_records_hashes_and_reclocks_only_heldout_vpos(tmp_path):
    options = fixtures(tmp_path)
    original = options["evaluation"]
    record = call_build(options)

    dense_path = options["output_dir"] / "heldout-dense-snapshot.json"
    dense = json.loads(dense_path.read_text(encoding="utf-8"))
    original_comments = [comment for thread in original["threads"] for comment in thread["comments"]]
    dense_comments = [comment for thread in dense["threads"] for comment in thread["comments"]]
    assert [comment["id"] for comment in dense_comments] == [comment["id"] for comment in original_comments]
    assert [comment["vposMs"] for comment in dense_comments] == [0, 1000, 1999]
    for before, after in zip(original_comments, dense_comments):
        assert {key: value for key, value in before.items() if key != "vposMs"} == {
            key: value for key, value in after.items() if key != "vposMs"
        }

    training = json.loads((options["output_dir"] / "training.json").read_text(encoding="utf-8"))
    evaluation = json.loads((options["output_dir"] / "evaluation.json").read_text(encoding="utf-8"))
    catalog = json.loads((options["output_dir"] / "workload-catalog.json").read_text(encoding="utf-8"))
    assert [item["coverage"] for item in training["materials"]] == list(MODULE.COVERAGE)
    assert [item["coverage"] for item in evaluation["materials"]] == list(MODULE.COVERAGE)
    train_ids = {comment["id"] for thread in json.loads(options["training_snapshot"].read_text())["threads"] for comment in thread["comments"]}
    eval_ids = {comment["id"] for thread in original["threads"] for comment in thread["comments"]}
    assert not train_ids & eval_ids
    assert record["sharedCommentIDCount"] == 0
    assert record["denseEvaluationDerivation"]["commentCount"] == len(eval_ids)
    dense_material = next(item for item in evaluation["materials"] if item["coverage"] == "dense")
    assert dense_material["snapshotSha256"] == record["denseEvaluationDerivation"]["derivedSnapshotSha256"]
    assert next(item for item in catalog["evaluation"] if item["coverage"] == "long")["fpsNum"] == 30
    assert record["encodedMediaWritten"] is False
    assert record["rawRGBAWritten"] is False
    assert not list(options["output_dir"].glob("*.mp4"))
    assert not list(options["output_dir"].glob("*.hls"))
    assert not list(options["output_dir"].glob("*.rgba"))
    owner = json.loads((options["output_dir"] / "pgo-input-owner.json").read_text(encoding="utf-8"))
    assert owner["kind"] == "nico-pgo-input-preparation"
    assert owner["status"] == "complete"


def test_preparation_rejects_comment_id_overlap_with_owned_preparation_marker(tmp_path):
    options = fixtures(tmp_path)
    write_snapshot(options["evaluation_snapshot"], ["train-a", "eval-b", "eval-c"])
    with pytest.raises(ValueError, match="share 1 comment IDs"):
        call_build(options)
    owner = json.loads((options["output_dir"] / "pgo-input-owner.json").read_text(encoding="utf-8"))
    assert owner["kind"] == "nico-pgo-input-preparation"
    assert owner["status"] == "preparing"


def test_dense_derivation_handles_a_single_comment_and_rejects_zero_window():
    snapshot = {"threads": [{"comments": [{"id": "one", "vposMs": 42, "body": "x"}]}]}
    assert MODULE.derive_dense_snapshot(snapshot, window_ms=1)["threads"][0]["comments"][0]["vposMs"] == 0
    with pytest.raises(ValueError, match="positive"):
        MODULE.derive_dense_snapshot(snapshot, window_ms=0)
