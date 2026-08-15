use crate::protocol::{FrameControl, ScrollProfile, TimelineChunk, TrackAssets};

#[derive(Debug, Clone, PartialEq)]
pub struct ResolvedFrameControl {
    pub sequence: u64,
    pub frame_index: u64,
    pub pts_ns: i64,
    pub feature_index: u32,
    pub spectrum_q16: Vec<u16>,
    pub waveform_q16: Vec<u16>,
    pub progress_q16: u16,
    pub text_alpha_q16: u16,
    pub black_alpha_q16: u16,
    pub scroll_x_q16: i32,
    pub scroll_y_q16: i32,
    pub scroll_alpha_q16: u16,
    pub scroll_clip: crate::contracts::SceneRect,
    pub loudness_q16: u16,
    pub loudness_trend_q16: u16,
}

fn find_profile<'a>(profiles: &'a [ScrollProfile], id: &str) -> Option<&'a ScrollProfile> {
    profiles.iter().find(|profile| profile.id == id)
}

fn resolve_frame(
    assets: &TrackAssets,
    profiles: &[ScrollProfile],
    frame: &FrameControl,
) -> Result<ResolvedFrameControl, String> {
    let profile = find_profile(profiles, &frame.scroll_profile)
        .ok_or_else(|| format!("unknown scroll profile {}", frame.scroll_profile))?;
    if profile.loop_frames == 0
        || profile.x_q16.len() as u64 != profile.loop_frames
        || profile.y_q16.len() as u64 != profile.loop_frames
        || profile.alpha_q16.len() as u64 != profile.loop_frames
    {
        return Err(format!(
            "scroll profile {} has invalid loop arrays",
            profile.id
        ));
    }
    let loop_index = (frame.frame_index % profile.loop_frames) as usize;
    let feature_index = frame.feature_index as usize;
    Ok(ResolvedFrameControl {
        sequence: frame.sequence,
        frame_index: frame.frame_index,
        pts_ns: frame.pts_ns,
        feature_index: frame.feature_index,
        spectrum_q16: frame.spectrum_q16.clone(),
        waveform_q16: frame.waveform_q16.clone(),
        progress_q16: frame.progress_q16,
        text_alpha_q16: frame.text_alpha_q16,
        black_alpha_q16: frame.black_alpha_q16,
        scroll_x_q16: profile.x_q16[loop_index],
        scroll_y_q16: profile.y_q16[loop_index],
        scroll_alpha_q16: profile.alpha_q16[loop_index],
        scroll_clip: profile.clip.clone(),
        loudness_q16: assets
            .loudness_envelope
            .get(feature_index)
            .copied()
            .unwrap_or(0),
        loudness_trend_q16: assets
            .loudness_trend
            .get(feature_index)
            .copied()
            .unwrap_or(0),
    })
}

pub fn resolve_chunk(
    assets: &TrackAssets,
    timeline: &TimelineChunk,
) -> Result<Vec<ResolvedFrameControl>, String> {
    if timeline.frames.is_empty() || timeline.frames.len() > crate::protocol::MAX_H264_BATCH_FRAMES
    {
        return Err("timeline chunk size is outside the bounded window".into());
    }
    if assets.duration_frames == 0 {
        return Err("track assets have no duration".into());
    }
    let mut resolved = Vec::with_capacity(timeline.frames.len());
    for frame in &timeline.frames {
        resolved.push(resolve_frame(assets, &timeline.scroll_profiles, frame)?);
    }
    Ok(resolved)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn assets() -> TrackAssets {
        TrackAssets {
            schema: crate::protocol::PROTOCOL_VERSION,
            track_id: "track".into(),
            assets_hash: "a".repeat(64),
            artwork: None,
            glyph_atlas: None,
            text_overlay: None,
            base_texture: None,
            waveform_texture: None,
            loudness_texture: None,
            spectrum_texture: None,
            layout: Default::default(),
            palette: Default::default(),
            loudness_envelope: vec![111, 222],
            loudness_trend: vec![11, 22],
            loudness_guides: [33, 44, 55, 66],
            duration_frames: 8,
        }
    }

    fn profile() -> ScrollProfile {
        ScrollProfile {
            id: "title".into(),
            fps: 30,
            loop_frames: 2,
            x_q16: vec![100, 200],
            y_q16: vec![300, 400],
            alpha_q16: vec![500, 600],
            clip: Default::default(),
            profile_hash: "b".repeat(64),
        }
    }

    fn frame(frame_index: u64, feature_index: u32) -> FrameControl {
        FrameControl {
            sequence: frame_index,
            frame_index,
            pts_ns: (frame_index as i64 + 1) * 1_000,
            feature_index,
            scroll_profile: "title".into(),
            spectrum_q16: vec![1, 2],
            waveform_q16: vec![3, 4],
            progress_q16: 7,
            text_alpha_q16: 8,
            black_alpha_q16: 9,
        }
    }

    #[test]
    fn resolves_scroll_loop_and_static_loudness_by_frame_index() {
        let chunk = TimelineChunk {
            scroll_profiles: vec![profile()],
            frames: vec![frame(0, 0), frame(3, 1)],
        };
        let resolved = resolve_chunk(&assets(), &chunk).unwrap();
        assert_eq!(resolved[0].scroll_x_q16, 100);
        assert_eq!(resolved[1].scroll_x_q16, 200);
        assert_eq!(resolved[0].loudness_q16, 111);
        assert_eq!(resolved[1].loudness_trend_q16, 22);
        assert_eq!(resolved[1].text_alpha_q16, 8);
    }

    #[test]
    fn unknown_scroll_profile_fails_closed() {
        let mut bad = frame(0, 0);
        bad.scroll_profile = "missing".into();
        let chunk = TimelineChunk {
            scroll_profiles: vec![profile()],
            frames: vec![bad],
        };
        let error = resolve_chunk(&assets(), &chunk).unwrap_err();
        assert!(error.contains("unknown scroll profile"));
    }

    #[test]
    fn oversized_chunk_is_rejected_before_resolution() {
        let chunk = TimelineChunk {
            scroll_profiles: vec![profile()],
            frames: (0..=crate::protocol::MAX_H264_BATCH_FRAMES as u64)
                .map(|index| frame(index, 0))
                .collect(),
        };
        assert!(resolve_chunk(&assets(), &chunk).is_err());
    }
}
