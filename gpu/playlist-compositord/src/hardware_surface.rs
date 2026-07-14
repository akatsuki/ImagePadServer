//! Optional hardware-surface interop contract for the zero-copy spike.
//!
//! This module deliberately contains capability descriptions only.  The
//! production path remains the bounded CPU readback ring (`GpuFrame`).  A
//! backend may implement `HardwareSurface` once encoder/import handles are
//! available for a target OS and driver; until then the probe must report
//! `blocked` rather than silently changing the default path.

use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Copy, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum SurfaceBackend {
    D3d12,
    Vulkan,
    Metal,
    Dx11,
    Unknown,
}

#[derive(Debug, Clone, Copy, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum SurfaceHandle {
    SharedTexture,
    DmaBuf,
    IOSurface,
    Opaque,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct SurfaceDescriptor {
    pub backend: SurfaceBackend,
    pub handle: SurfaceHandle,
    pub width: u32,
    pub height: u32,
    pub format: String,
    pub color_space: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ZeroCopyEvidence {
    pub schema: u16,
    pub status: String,
    pub reason: String,
    pub backend: Option<SurfaceBackend>,
    pub handle: Option<SurfaceHandle>,
    pub frames: u64,
    pub elapsed_ms: u64,
    pub readback_bytes: u64,
    pub imported_frames: u64,
    pub copy_bytes: u64,
    pub p50_ms: Option<f64>,
    pub p95_ms: Option<f64>,
}

pub const ZERO_COPY_EVIDENCE_SCHEMA: u16 = 1;

/// Describes an implementation-specific surface import/export bridge.
/// Implementations must not be used unless `probe` returns `Ready` and the
/// encoder accepts the exact descriptor.  This trait is intentionally not
/// wired into the compositor yet.
pub trait HardwareSurface {
    fn descriptor(&self) -> SurfaceDescriptor;
    fn probe(&self) -> SurfaceProbe;
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum SurfaceProbe {
    Ready,
    Blocked,
    Unsupported,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn evidence_schema_round_trips_blocked_probe() {
        let e = ZeroCopyEvidence {
            schema: ZERO_COPY_EVIDENCE_SCHEMA,
            status: "BLOCKED".into(),
            reason: "encoder_import_interop_unavailable".into(),
            backend: Some(SurfaceBackend::D3d12),
            handle: Some(SurfaceHandle::SharedTexture),
            frames: 0,
            elapsed_ms: 0,
            readback_bytes: 0,
            imported_frames: 0,
            copy_bytes: 0,
            p50_ms: None,
            p95_ms: None,
        };
        let json = serde_json::to_string(&e).unwrap();
        assert_eq!(serde_json::from_str::<ZeroCopyEvidence>(&json).unwrap(), e);
    }
}
