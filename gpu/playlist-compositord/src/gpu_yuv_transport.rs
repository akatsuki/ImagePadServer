/// GPU-owned YUV resources passed to an encoder bridge without CPU mapping.
/// The handle is opaque to the compositor; backend-specific external-memory
/// export is supplied by the platform adapter.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct GpuYuvFrameHandle {
    pub sequence: u64,
    pub pts_ns: i64,
    pub width: u32,
    pub height: u32,
    pub y_stride: u32,
    pub u_stride: u32,
    pub v_stride: u32,
    pub adapter: String,
    pub backend: String,
}

pub trait GpuYuvEncoderSink {
    type Error;

    fn submit_gpu_yuv(&mut self, frame: GpuYuvFrameHandle) -> Result<(), Self::Error>;
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn handle_has_no_cpu_plane_payload() {
        let frame = GpuYuvFrameHandle {
            sequence: 1,
            pts_ns: 0,
            width: 1279,
            height: 719,
            y_stride: 1279,
            u_stride: 640,
            v_stride: 640,
            adapter: "test".into(),
            backend: "vulkan".into(),
        };
        assert_eq!(frame.width, 1279);
        assert_eq!(frame.u_stride, (frame.width + 1) / 2);
    }
}
