#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Pass {
    ArtworkPreprocess,
    Background,
    Spectrum,
    Waveform,
    Loudness,
    TextUi,
    Progress,
    Fade,
    Yuv420,
}

/// Canonical CPU reference order. The implementation is intentionally kept as
/// data first so each pass can be replaced by a native shader and compared in
/// isolation without changing composition order.
pub const CANONICAL_ORDER: [Pass; 9] = [
    Pass::ArtworkPreprocess,
    Pass::Background,
    Pass::Spectrum,
    Pass::Waveform,
    Pass::Loudness,
    Pass::TextUi,
    Pass::Progress,
    Pass::Fade,
    Pass::Yuv420,
];

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn order_has_single_yuv_sink_and_ui_after_dynamic_layers() {
        assert_eq!(CANONICAL_ORDER[0], Pass::ArtworkPreprocess);
        assert_eq!(CANONICAL_ORDER[5], Pass::TextUi);
        assert_eq!(CANONICAL_ORDER[8], Pass::Yuv420);
        assert_eq!(
            CANONICAL_ORDER
                .iter()
                .filter(|p| **p == Pass::Yuv420)
                .count(),
            1
        );
    }
}
