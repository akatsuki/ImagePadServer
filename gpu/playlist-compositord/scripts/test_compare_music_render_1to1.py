from __future__ import annotations

import importlib.util
import sys
from pathlib import Path


SCRIPT = Path(__file__).with_name("compare_music_render_1to1.py")
SPEC = importlib.util.spec_from_file_location("compare_music_render_1to1", SCRIPT)
assert SPEC is not None and SPEC.loader is not None
MODULE = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = MODULE
SPEC.loader.exec_module(MODULE)


def test_gpu_command_preserves_one_to_one_conditions_and_scene_inputs() -> None:
    config = MODULE.ComparisonConfig(
        width=640,
        height=360,
        fps=30,
        frames=301,
        pcm_samples=4096,
        sample_rate=44100,
        pcm_input=Path("input.f32le"),
        title="odd - 2017_10_22 6.59",
        artist="赤月",
        album="",
        artwork_input=Path("artwork.rgba"),
        output_dir=Path("out"),
    )

    args = MODULE.build_gpu_export_args(
        Path("playlist-compositord.exe"), config, Path("out.yuv"), Path("shared-scene.json")
    )

    assert args[0:3] == ["playlist-compositord.exe", "--export-draft-shader-yuv", "--width"]
    assert "--height" in args and args[args.index("--height") + 1] == "360"
    assert "--fps" in args and args[args.index("--fps") + 1] == "30"
    assert "--frames" in args and args[args.index("--frames") + 1] == "301"
    assert "--pcm-input" in args and args[args.index("--pcm-input") + 1] == "input.f32le"
    assert "--scene-input" in args and args[args.index("--scene-input") + 1] == "shared-scene.json"
    assert "--title" in args and args[args.index("--title") + 1] == "odd - 2017_10_22 6.59"
    assert "--artist" in args and args[args.index("--artist") + 1] == "赤月"
    assert "--artwork-input" in args and args[args.index("--artwork-input") + 1] == "artwork.rgba"


def test_parse_frame_stats_counts_exact_frame_indices() -> None:
    stats = """n:1 mse_avg:10.000000 n:2 mse_avg:20.000000 n:3 mse_avg:30.000000"""

    parsed = MODULE.parse_frame_stats(stats)

    assert parsed.frame_count == 3
    assert parsed.first_frame == 1
    assert parsed.last_frame == 3
    assert parsed.mean_mse == 20.0


def test_scene_command_uses_the_same_conditions_as_the_renderers() -> None:
    config = MODULE.ComparisonConfig(
        width=640,
        height=360,
        fps=30,
        frames=301,
        pcm_samples=4096,
        sample_rate=44100,
        pcm_input=Path("input.f32le"),
        title="title",
        artist="artist",
        album="album",
        artwork_input=Path("artwork.rgba"),
        output_dir=Path("out"),
    )

    args = MODULE._build_scene_command(
        "go", config, Path("audio.m4a"), Path("cover.jpg"), Path("shared-scene.json"), "ffmpeg"
    )

    assert args[:3] == ["go", "run", "./cmd/music-scene-export"]
    assert args[args.index("--width") + 1] == "640"
    assert args[args.index("--frames") + 1] == "301"
    assert args[args.index("--sample-rate") + 1] == "44100"
    assert args[args.index("--output") + 1] == "shared-scene.json"
    assert args[args.index("--ffmpeg") + 1] == "ffmpeg"


def test_gpu_mux_preserves_requested_frame_count_instead_of_audio_shortest() -> None:
    config = MODULE.ComparisonConfig(
        width=640,
        height=360,
        fps=30,
        frames=301,
        pcm_samples=4096,
        sample_rate=44100,
        pcm_input=Path("input.f32le"),
        title="title",
        artist="artist",
        album="album",
        artwork_input=Path("artwork.rgba"),
        output_dir=Path("out"),
    )

    args = MODULE.build_gpu_mux_args(
        "ffmpeg",
        config,
        Path("gpu.yuv420p"),
        Path("audio.m4a"),
        Path("gpu.mp4"),
    )

    assert "-shortest" not in args
    assert "-frames:v" in args
    assert args[args.index("-frames:v") + 1] == "301"


def test_gpu_pipe_mux_reads_raw_yuv_from_stdin() -> None:
    config = MODULE.ComparisonConfig(
        width=640,
        height=360,
        fps=30,
        frames=301,
        pcm_samples=4096,
        sample_rate=44100,
        pcm_input=Path("input.f32le"),
        title="title",
        artist="artist",
        album="",
        artwork_input=Path("artwork.rgba"),
        output_dir=Path("out"),
    )

    args = MODULE.build_gpu_pipe_mux_args(
        "ffmpeg",
        config,
        Path("audio.m4a"),
        Path("gpu.mp4"),
    )

    input_index = args.index("-i")
    assert args[input_index + 1] == "-"
    assert args[args.index("-frames:v") + 1] == "301"


def test_manifest_marks_diagnostic_gpu_boundary() -> None:
    manifest = MODULE.build_manifest(
        MODULE.ComparisonConfig(
            width=640,
            height=360,
            fps=30,
            frames=301,
            pcm_samples=4096,
            sample_rate=44100,
            pcm_input=Path("input.f32le"),
            title="title",
            artist="artist",
            album="album",
            artwork_input=Path("artwork.rgba"),
            output_dir=Path("out"),
        ),
        cpu_wall_seconds=1.0,
        gpu_render_seconds=2.0,
        gpu_mux_seconds=0.5,
        cpu_frames=301,
        gpu_frames=301,
        comparison=MODULE.FrameStats(frame_count=301, first_frame=1, last_frame=301, mean_mse=0.0),
    )

    assert manifest["conditions"]["width"] == 640
    assert manifest["conditions"]["fps"] == 30
    assert manifest["conditions"]["frames"] == 301
    assert manifest["gpu"]["production_ready"] is False
    assert manifest["gpu"]["readback"] is True
    assert manifest["comparison"]["frame_count_match"] is True


if __name__ == "__main__":
    for test in (
        test_gpu_command_preserves_one_to_one_conditions_and_scene_inputs,
        test_parse_frame_stats_counts_exact_frame_indices,
        test_scene_command_uses_the_same_conditions_as_the_renderers,
        test_gpu_mux_preserves_requested_frame_count_instead_of_audio_shortest,
        test_gpu_pipe_mux_reads_raw_yuv_from_stdin,
        test_manifest_marks_diagnostic_gpu_boundary,
    ):
        test()
    print("focused compare tests: PASS (6/6)")
