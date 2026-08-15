#[derive(Clone, Debug, PartialEq, Eq)]
pub struct DirectEncoderContract {
    pub width: u32,
    pub height: u32,
    pub backend: String,
    pub frames: u64,
    pub pixel_readback_bytes: u64,
}

impl DirectEncoderContract {
    pub fn new(
        width: u32,
        height: u32,
        backend: impl Into<String>,
        frames: u64,
        pixel_readback_bytes: u64,
    ) -> Self {
        Self {
            width,
            height,
            backend: backend.into(),
            frames,
            pixel_readback_bytes,
        }
    }

    pub fn validate(&self) -> Result<(), String> {
        if self.backend != "dx12" {
            return Err("direct NVENC requires the D3D12 backend".into());
        }
        if self.pixel_readback_bytes != 0 {
            return Err("direct NVENC cannot accept a pixel readback".into());
        }
        if self.width == 0 || self.height == 0 || self.frames == 0 {
            return Err("direct NVENC dimensions and frame count must be non-zero".into());
        }
        Ok(())
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct DirectEncoderProbeConfig {
    pub width: u32,
    pub height: u32,
    pub fps: u32,
    pub output: std::path::PathBuf,
}

pub fn parse_probe_args(args: &[String]) -> Result<DirectEncoderProbeConfig, String> {
    let mut width = 640;
    let mut height = 360;
    let mut fps = 30;
    let mut output = None;
    let mut index = 0;
    while index < args.len() {
        let flag = args[index].as_str();
        let value = |index: &mut usize| -> Result<&str, String> {
            *index += 1;
            args.get(*index)
                .map(String::as_str)
                .ok_or_else(|| format!("missing value for {flag}"))
        };
        match flag {
            "--width" => {
                width = value(&mut index)?
                    .parse()
                    .map_err(|error| format!("invalid --width: {error}"))?;
            }
            "--height" => {
                height = value(&mut index)?
                    .parse()
                    .map_err(|error| format!("invalid --height: {error}"))?;
            }
            "--fps" => {
                fps = value(&mut index)?
                    .parse()
                    .map_err(|error| format!("invalid --fps: {error}"))?;
            }
            "--output" => output = Some(std::path::PathBuf::from(value(&mut index)?)),
            other => return Err(format!("unknown NVENC probe option: {other}")),
        }
        index += 1;
    }
    let output = output.ok_or_else(|| "--output is required".to_string())?;
    if width == 0 || height == 0 || fps == 0 {
        return Err("NVENC probe dimensions and fps must be non-zero".into());
    }
    Ok(DirectEncoderProbeConfig {
        width,
        height,
        fps,
        output,
    })
}

#[cfg(windows)]
mod windows_impl {
    use super::DirectEncoderContract;
    use d3d12::{ComPtr, Fence, Resource};
    use moq_nvenc::{
        safe::ENCODE_API,
        sys::nvEncodeAPI::{
            NVENCAPI_STRUCT_VERSION, NVENCAPI_VERSION, NVENCSTATUS, NV_ENC_BUFFER_FORMAT,
            NV_ENC_BUFFER_USAGE, NV_ENC_CODEC_H264_GUID, NV_ENC_CONFIG, NV_ENC_CONFIG_VER,
            NV_ENC_DEVICE_TYPE, NV_ENC_FENCE_POINT_D3D12, NV_ENC_FENCE_POINT_D3D12_VER,
            NV_ENC_INITIALIZE_PARAMS, NV_ENC_INITIALIZE_PARAMS_VER, NV_ENC_INPUT_RESOURCE_D3D12,
            NV_ENC_INPUT_RESOURCE_D3D12_VER, NV_ENC_INPUT_RESOURCE_TYPE, NV_ENC_MAP_INPUT_RESOURCE,
            NV_ENC_MAP_INPUT_RESOURCE_VER, NV_ENC_OUTPUT_RESOURCE_D3D12,
            NV_ENC_OUTPUT_RESOURCE_D3D12_VER, NV_ENC_PARAMS_RC_MODE, NV_ENC_PIC_FLAGS,
            NV_ENC_PIC_PARAMS, NV_ENC_PIC_PARAMS_VER, NV_ENC_PIC_STRUCT, NV_ENC_PRESET_CONFIG,
            NV_ENC_PRESET_CONFIG_VER, NV_ENC_QP, NV_ENC_REGISTER_RESOURCE,
            NV_ENC_REGISTER_RESOURCE_VER, NV_ENC_TUNING_INFO, NV_ENC_VUI_MATRIX_COEFFS,
        },
    };
    use std::ffi::{c_void, CStr};
    use std::ptr::{null, null_mut};
    use wgpu::hal::api::Dx12;
    use wgpu::hal::dx12::Device as Dx12Device;
    use winapi::shared::dxgiformat::{DXGI_FORMAT_B8G8R8A8_UNORM, DXGI_FORMAT_UNKNOWN};
    use winapi::shared::dxgitype::DXGI_SAMPLE_DESC;
    use winapi::shared::winerror::SUCCEEDED;
    use winapi::um::d3d12::{
        D3D12_FENCE_FLAG_NONE, D3D12_HEAP_FLAG_NONE, D3D12_HEAP_PROPERTIES,
        D3D12_HEAP_TYPE_DEFAULT, D3D12_HEAP_TYPE_READBACK, D3D12_RESOURCE_DESC,
        D3D12_RESOURCE_DIMENSION_BUFFER, D3D12_RESOURCE_DIMENSION_TEXTURE2D,
        D3D12_RESOURCE_FLAG_ALLOW_RENDER_TARGET, D3D12_RESOURCE_FLAG_NONE,
        D3D12_RESOURCE_STATE_COPY_DEST, D3D12_RESOURCE_STATE_RENDER_TARGET,
        D3D12_TEXTURE_LAYOUT_ROW_MAJOR, D3D12_TEXTURE_LAYOUT_UNKNOWN,
    };
    use winapi::Interface;

    struct NvencSessionGuard(*mut c_void);

    impl Drop for NvencSessionGuard {
        fn drop(&mut self) {
            if !self.0.is_null() {
                let _ = unsafe { (ENCODE_API.destroy_encoder)(self.0) };
            }
        }
    }

    unsafe fn com_ptr_from_owned<T: Interface>(raw: *mut T) -> ComPtr<T> {
        let ptr = ComPtr::from_raw(raw);
        if !raw.is_null() {
            (*(raw.cast::<winapi::um::unknwnbase::IUnknown>())).Release();
        }
        ptr
    }

    /// A native D3D12 RGBA texture owned by the same device as wgpu. The
    /// resource is retained separately from the wgpu wrapper so NVENC can
    /// register the exact `ID3D12Resource*` without any pixel mapping.
    pub struct D3d12Surface {
        pub texture: wgpu::Texture,
        resource: Resource,
        pub width: u32,
        pub height: u32,
    }

    impl std::fmt::Debug for D3d12Surface {
        fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
            formatter
                .debug_struct("D3d12Surface")
                .field("width", &self.width)
                .field("height", &self.height)
                .finish_non_exhaustive()
        }
    }

    impl D3d12Surface {
        pub fn create(device: &wgpu::Device, width: u32, height: u32) -> Result<Self, String> {
            if width == 0 || height == 0 {
                return Err("D3D12 encoder surface dimensions must be non-zero".into());
            }

            let descriptor = wgpu::TextureDescriptor {
                label: Some("nvenc-d3d12-rgba-surface"),
                size: wgpu::Extent3d {
                    width,
                    height,
                    depth_or_array_layers: 1,
                },
                mip_level_count: 1,
                sample_count: 1,
                dimension: wgpu::TextureDimension::D2,
                format: wgpu::TextureFormat::Bgra8Unorm,
                usage: wgpu::TextureUsages::RENDER_ATTACHMENT,
                view_formats: &[],
            };

            let (resource, hal_texture) = unsafe {
                device
                    .as_hal::<Dx12, _, _>(|hal_device| {
                        let hal_device = hal_device.ok_or_else(|| {
                            "wgpu device is not using the D3D12 backend".to_string()
                        })?;
                        let resource =
                            create_rgba_resource(hal_device.raw_device(), width, height)?;
                        let hal_texture = Dx12Device::texture_from_raw(
                            resource.clone(),
                            wgpu::TextureFormat::Bgra8Unorm,
                            wgpu::TextureDimension::D2,
                            descriptor.size,
                            1,
                            1,
                        );
                        Ok::<_, String>((resource, hal_texture))
                    })
                    .ok_or_else(|| "wgpu core does not expose the D3D12 device".to_string())??
            };

            // `texture_from_raw` is deliberately called while the HAL device
            // lock is held. Creating the public wgpu object afterwards avoids
            // recursively acquiring that lock through `create_texture_from_hal`.
            let texture =
                unsafe { device.create_texture_from_hal::<Dx12>(hal_texture, &descriptor) };
            Ok(Self {
                texture,
                resource,
                width,
                height,
            })
        }

        pub fn native_resource(&self) -> *mut c_void {
            self.resource.as_mut_ptr().cast()
        }

        pub fn contract(&self, frames: u64) -> DirectEncoderContract {
            DirectEncoderContract::new(self.width, self.height, "dx12", frames, 0)
        }
    }

    pub fn nvenc_runtime_available() -> bool {
        ["nvEncodeAPI64.dll", "nvEncodeAPI.dll"]
            .iter()
            .any(|name| unsafe { libloading::Library::new(*name).is_ok() })
    }

    struct RegisteredInput {
        registered_resource: *mut c_void,
        mapped_resource: *mut c_void,
    }

    struct RegisteredOutput {
        resource: Resource,
        fence: Fence,
        registered_resource: *mut c_void,
        mapped_resource: *mut c_void,
    }

    pub struct PendingBitstream {
        pub slot: usize,
        pub frame_index: u32,
        output_fence_value: u64,
    }

    pub struct D3d12NvencEncoder {
        session: *mut c_void,
        input_fence: Fence,
        output_fence: Fence,
        output_resource: Resource,
        output_registered: *mut c_void,
        output_mapped: *mut c_void,
        output_capacity: u64,
        width: u32,
        height: u32,
        buffer_format: NV_ENC_BUFFER_FORMAT,
        input_resources: Vec<RegisteredInput>,
        output_resources: Vec<RegisteredOutput>,
        last_frame_index: Option<u32>,
        last_output_slot: Option<usize>,
        last_submitted_frame_index: Option<u32>,
        eos_sent: bool,
    }

    impl std::fmt::Debug for D3d12NvencEncoder {
        fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
            formatter
                .debug_struct("D3d12NvencEncoder")
                .field("width", &self.width)
                .field("height", &self.height)
                .finish_non_exhaustive()
        }
    }

    impl D3d12NvencEncoder {
        pub fn create(
            device: &wgpu::Device,
            width: u32,
            height: u32,
            fps: u32,
        ) -> Result<Self, String> {
            if !nvenc_runtime_available() {
                return Err("NVENC runtime DLL is unavailable".into());
            }
            if width == 0 || height == 0 || fps == 0 {
                return Err("NVENC dimensions and fps must be non-zero".into());
            }
            let (
                session,
                input_fence,
                output_fence,
                output_resource,
                output_registered,
                output_mapped,
                output_capacity,
                buffer_format,
            ) = unsafe {
                device
                    .as_hal::<Dx12, _, _>(|hal_device| {
                        let hal_device = hal_device
                            .ok_or_else(|| "NVENC requires a D3D12 wgpu device".to_string())?;
                        let mut open_params = moq_nvenc::sys::nvEncodeAPI::NV_ENC_OPEN_ENCODE_SESSION_EX_PARAMS {
                            version: moq_nvenc::sys::nvEncodeAPI::NV_ENC_OPEN_ENCODE_SESSION_EX_PARAMS_VER,
                            deviceType: NV_ENC_DEVICE_TYPE::NV_ENC_DEVICE_TYPE_DIRECTX,
                            device: hal_device.raw_device().as_mut_ptr().cast(),
                            apiVersion: NVENCAPI_VERSION,
                            ..Default::default()
                        };
                        let mut session = null_mut();
                        let open = ENCODE_API.open_encode_session_ex;
                        check_status("NvEncOpenEncodeSessionEx", open(&mut open_params, &mut session))?;
                        if session.is_null() {
                            return Err("NvEncOpenEncodeSessionEx returned a null session".into());
                        }
                        let _session_guard = NvencSessionGuard(session);
                        let input_fence = create_fence(hal_device.raw_device())?;
                        let output_fence = create_fence(hal_device.raw_device())?;

                        let mut format_count = 0u32;
                        check_status(
                            "NvEncGetInputFormatCount",
                            (ENCODE_API.get_input_format_count)(
                                session,
                                NV_ENC_CODEC_H264_GUID,
                                &mut format_count,
                            ),
                        )?;
                        let mut supported_formats = vec![
                            NV_ENC_BUFFER_FORMAT::NV_ENC_BUFFER_FORMAT_UNDEFINED;
                            format_count as usize
                        ];
                        let mut actual_format_count = 0u32;
                        if format_count > 0 {
                            check_status(
                                "NvEncGetInputFormats",
                                (ENCODE_API.get_input_formats)(
                                    session,
                                    NV_ENC_CODEC_H264_GUID,
                                    supported_formats.as_mut_ptr(),
                                    supported_formats.len() as u32,
                                    &mut actual_format_count,
                                ),
                            )?;
                            supported_formats.truncate(actual_format_count as usize);
                        }
                        eprintln!("nvenc_h264_supported_input_formats={supported_formats:?}");
                        let buffer_format = supported_formats
                            .iter()
                            .copied()
                            .find(|format| {
                                *format == NV_ENC_BUFFER_FORMAT::NV_ENC_BUFFER_FORMAT_ARGB
                            })
                            .ok_or_else(|| {
                                format!(
                                    "NVENC H.264 does not support an RGBA-compatible input format: {supported_formats:?}"
                                )
                            })?;

                        let mut preset_config = NV_ENC_PRESET_CONFIG {
                            version: NV_ENC_PRESET_CONFIG_VER,
                            presetCfg: NV_ENC_CONFIG {
                                version: NV_ENC_CONFIG_VER,
                                ..Default::default()
                            },
                            ..Default::default()
                        };
                        let preset_fn = ENCODE_API.get_encode_preset_config_ex;
                        check_status(
                            "NvEncGetEncodePresetConfigEx",
                            preset_fn(
                                session,
                                NV_ENC_CODEC_H264_GUID,
                                moq_nvenc::sys::nvEncodeAPI::NV_ENC_PRESET_P1_GUID,
                                NV_ENC_TUNING_INFO::NV_ENC_TUNING_INFO_ULTRA_LOW_LATENCY,
                                &mut preset_config,
                            ),
                        )?;
                        preset_config.presetCfg.frameIntervalP = 1;
                        preset_config.presetCfg.gopLength = u32::MAX;
                        preset_config.presetCfg.rcParams.rateControlMode =
                            NV_ENC_PARAMS_RC_MODE::NV_ENC_PARAMS_RC_CONSTQP;
                        let parity_lossless = std::env::var("IMAGEPAD_GPU_PARITY_LOSSLESS")
                            .ok()
                            .as_deref()
                            == Some("1");
                        let (qp_inter_p, qp_inter_b, qp_intra) = super::constant_qp_values(parity_lossless);
                        preset_config.presetCfg.rcParams.constQP = NV_ENC_QP {
                            qpInterP: qp_inter_p,
                            qpInterB: qp_inter_b,
                            qpIntra: qp_intra,
                        };
                        preset_config.presetCfg.encodeCodecConfig.h264Config.idrPeriod = u32::MAX;
                        // Re-emit SPS/PPS before every IDR. A reused encoder
                        // session must start each new track with a fresh,
                        // independently-decodable GOP: the muxed artifact is
                        // probed from the track's first IDR, which otherwise
                        // lacks the SPS/PPS that were only sent at session init.
                        preset_config
                            .presetCfg
                            .encodeCodecConfig
                            .h264Config
                            .set_repeatSPSPPS(1);
                        preset_config
                            .presetCfg
                            .encodeCodecConfig
                            .h264Config
                            .h264VUIParameters
                            .colourMatrix = NV_ENC_VUI_MATRIX_COEFFS::NV_ENC_VUI_MATRIX_COEFFS_BT709;
                        let mut initialize = NV_ENC_INITIALIZE_PARAMS {
                            version: NV_ENC_INITIALIZE_PARAMS_VER,
                            encodeGUID: NV_ENC_CODEC_H264_GUID,
                            presetGUID: moq_nvenc::sys::nvEncodeAPI::NV_ENC_PRESET_P1_GUID,
                            encodeWidth: width,
                            encodeHeight: height,
                            darWidth: width,
                            darHeight: height,
                            frameRateNum: fps,
                            frameRateDen: 1,
                            enableEncodeAsync: 1,
                            enablePTD: 1,
                            encodeConfig: &mut preset_config.presetCfg,
                            maxEncodeWidth: width,
                            maxEncodeHeight: height,
                            tuningInfo: NV_ENC_TUNING_INFO::NV_ENC_TUNING_INFO_ULTRA_LOW_LATENCY,
                            bufferFormat: buffer_format,
                            ..Default::default()
                        };

                        let initialize_fn = ENCODE_API.initialize_encoder;
                        if let Err(error) = check_status(
                            "NvEncInitializeEncoder",
                            initialize_fn(session, &mut initialize),
                        ) {
                            let detail_ptr = (ENCODE_API.get_last_error_string)(session);
                            let detail = if detail_ptr.is_null() {
                                "no NVENC error detail".to_string()
                            } else {
                                unsafe { CStr::from_ptr(detail_ptr) }
                                    .to_string_lossy()
                                    .into_owned()
                            };
                            return Err(format!("{error}; driver_detail={detail}"));
                        }

                        let output_capacity = u64::from(width)
                            .checked_mul(u64::from(height))
                            .and_then(|pixels| pixels.checked_mul(8))
                            .ok_or_else(|| "NVENC output buffer size overflow".to_string())?;
                        let output_resource =
                            create_bitstream_resource(hal_device.raw_device(), output_capacity)?;
                        let mut output_register = NV_ENC_REGISTER_RESOURCE {
                            version: NV_ENC_REGISTER_RESOURCE_VER,
                            resourceType: NV_ENC_INPUT_RESOURCE_TYPE::NV_ENC_INPUT_RESOURCE_TYPE_DIRECTX,
                            width: output_capacity as u32,
                            height: 1,
                            pitch: 0,
                            resourceToRegister: output_resource.as_mut_ptr().cast(),
                            bufferFormat: NV_ENC_BUFFER_FORMAT::NV_ENC_BUFFER_FORMAT_U8,
                            bufferUsage: NV_ENC_BUFFER_USAGE::NV_ENC_OUTPUT_BITSTREAM,
                            ..Default::default()
                        };
                        let output_register_status =
                            (ENCODE_API.register_resource)(session, &mut output_register);
                        if let Err(error) = check_status(
                            "NvEncRegisterResource(output bitstream)",
                            output_register_status,
                        ) {
                            let detail_ptr = (ENCODE_API.get_last_error_string)(session);
                            let detail = if detail_ptr.is_null() {
                                "no NVENC error detail".to_string()
                            } else {
                                CStr::from_ptr(detail_ptr).to_string_lossy().into_owned()
                            };
                            return Err(format!(
                                "{error}; driver_detail={detail}; register_version=0x{:08x}; resource={:?}; width={}; height={}; pitch={}; format={:?}; usage={:?}",
                                output_register.version,
                                output_register.resourceToRegister,
                                output_register.width,
                                output_register.height,
                                output_register.pitch,
                                output_register.bufferFormat,
                                output_register.bufferUsage,
                            ));
                        }
                        let mut output_map = NV_ENC_MAP_INPUT_RESOURCE {
                            version: NV_ENC_MAP_INPUT_RESOURCE_VER,
                            registeredResource: output_register.registeredResource,
                            ..Default::default()
                        };
                        if let Err(error) = check_status(
                            "NvEncMapInputResource(output bitstream)",
                            (ENCODE_API.map_input_resource)(session, &mut output_map),
                        ) {
                            let _ = (ENCODE_API.unregister_resource)(
                                session,
                                output_register.registeredResource,
                            );
                            return Err(error);
                        }
                        std::mem::forget(_session_guard);
                        Ok::<_, String>((
                            session,
                            input_fence,
                            output_fence,
                            output_resource,
                            output_register.registeredResource,
                            output_map.mappedResource,
                            output_capacity,
                            buffer_format,
                        ))
                    })
                    .ok_or_else(|| "wgpu core does not expose the D3D12 device".to_string())??
            };
            Ok(Self {
                session,
                input_fence,
                output_fence,
                output_resource,
                output_registered,
                output_mapped,
                output_capacity,
                width,
                height,
                buffer_format,
                input_resources: Vec::new(),
                output_resources: Vec::new(),
                last_frame_index: None,
                last_output_slot: None,
                last_submitted_frame_index: None,
                eos_sent: false,
            })
        }

        pub fn register_output_slot(&mut self, device: &wgpu::Device) -> Result<usize, String> {
            let session = self.session;
            let capacity = self.output_capacity;
            let (resource, fence, registered_resource, mapped_resource) = unsafe {
                device
                    .as_hal::<Dx12, _, _>(|hal_device| {
                        let hal_device = hal_device.ok_or_else(|| {
                            "NVENC output slot requires a D3D12 wgpu device".to_string()
                        })?;
                        let fence = create_fence(hal_device.raw_device())?;
                        let resource =
                            create_bitstream_resource(hal_device.raw_device(), capacity)?;
                        let mut register = NV_ENC_REGISTER_RESOURCE {
                            version: NV_ENC_REGISTER_RESOURCE_VER,
                            resourceType:
                                NV_ENC_INPUT_RESOURCE_TYPE::NV_ENC_INPUT_RESOURCE_TYPE_DIRECTX,
                            width: capacity as u32,
                            height: 1,
                            pitch: 0,
                            resourceToRegister: resource.as_mut_ptr().cast(),
                            bufferFormat: NV_ENC_BUFFER_FORMAT::NV_ENC_BUFFER_FORMAT_U8,
                            bufferUsage: NV_ENC_BUFFER_USAGE::NV_ENC_OUTPUT_BITSTREAM,
                            ..Default::default()
                        };
                        check_status(
                            "NvEncRegisterResource(output ring slot)",
                            (ENCODE_API.register_resource)(session, &mut register),
                        )?;
                        let mut map = NV_ENC_MAP_INPUT_RESOURCE {
                            version: NV_ENC_MAP_INPUT_RESOURCE_VER,
                            registeredResource: register.registeredResource,
                            ..Default::default()
                        };
                        if let Err(error) = check_status(
                            "NvEncMapInputResource(output ring slot)",
                            (ENCODE_API.map_input_resource)(session, &mut map),
                        ) {
                            let _ = (ENCODE_API.unregister_resource)(
                                session,
                                register.registeredResource,
                            );
                            return Err(error);
                        }
                        Ok::<_, String>((
                            resource,
                            fence,
                            register.registeredResource,
                            map.mappedResource,
                        ))
                    })
                    .ok_or_else(|| "wgpu core does not expose the D3D12 device".to_string())??
            };
            self.output_resources.push(RegisteredOutput {
                resource,
                fence,
                registered_resource,
                mapped_resource,
            });
            Ok(self.output_resources.len())
        }

        /// Register and map the persistent D3D12 input surface once. The
        /// previous diagnostic loop registered/mapped/unmapped/unregistered
        /// the same texture for every frame, which hid the actual encode cost
        /// behind avoidable NVENC resource-management calls.
        pub fn register_surface(&mut self, surface: &D3d12Surface) -> Result<(), String> {
            self.register_surface_at(self.input_resources.len(), surface)
        }

        /// Register one persistent input surface for a ring slot. A resource
        /// remains registered for the entire encoder lifetime and is never
        /// reused for another slot, so NVENC cannot observe a surface while a
        /// later frame is rendering into it.
        pub fn register_surface_at(
            &mut self,
            slot: usize,
            surface: &D3d12Surface,
        ) -> Result<(), String> {
            if slot != self.input_resources.len() {
                return Err(format!(
                    "NVENC input surface slots must be registered contiguously: expected {}, got {}",
                    self.input_resources.len(),
                    slot
                ));
            }
            let mut register = NV_ENC_REGISTER_RESOURCE {
                version: NV_ENC_REGISTER_RESOURCE_VER,
                resourceType: NV_ENC_INPUT_RESOURCE_TYPE::NV_ENC_INPUT_RESOURCE_TYPE_DIRECTX,
                width: self.width,
                height: self.height,
                pitch: 0,
                resourceToRegister: surface.native_resource(),
                bufferFormat: self.buffer_format,
                bufferUsage: NV_ENC_BUFFER_USAGE::NV_ENC_INPUT_IMAGE,
                ..Default::default()
            };
            let register_fn = ENCODE_API.register_resource;
            check_status("NvEncRegisterResource(input image)", unsafe {
                register_fn(self.session, &mut register)
            })?;

            let mut map = NV_ENC_MAP_INPUT_RESOURCE {
                version: NV_ENC_MAP_INPUT_RESOURCE_VER,
                registeredResource: register.registeredResource,
                ..Default::default()
            };
            if let Err(error) = check_status("NvEncMapInputResource", unsafe {
                (ENCODE_API.map_input_resource)(self.session, &mut map)
            }) {
                self.unregister(register.registeredResource);
                return Err(error);
            }
            self.input_resources.push(RegisteredInput {
                registered_resource: register.registeredResource,
                mapped_resource: map.mappedResource,
            });
            Ok(())
        }

        /// Signal the D3D12 queue after the wgpu command list that rendered
        /// the encoder surface. NVENC consumes the same fence/value through
        /// `NV_ENC_INPUT_RESOURCE_D3D12::inputFencePoint`, so the handoff is
        /// GPU-ordered instead of relying on a host-side device wait.
        pub fn signal_input_fence(&self, device: &wgpu::Device, value: u64) -> Result<(), String> {
            if value == 0 {
                return Err("NVENC input fence value must be non-zero".into());
            }
            let result = unsafe {
                device
                    .as_hal::<Dx12, _, _>(|hal_device| {
                        let hal_device = hal_device.ok_or_else(|| {
                            "NVENC input fence requires a D3D12 wgpu device".to_string()
                        })?;
                        Ok::<_, String>(hal_device.raw_queue().signal(&self.input_fence, value))
                    })
                    .ok_or_else(|| "wgpu core does not expose the D3D12 queue".to_string())??
            };
            if !SUCCEEDED(result) {
                return Err(format!(
                    "ID3D12CommandQueue::Signal(NVENC input fence) failed: HRESULT 0x{result:08x}"
                ));
            }
            Ok(())
        }

        pub fn input_fence_completed_value(&mut self) -> u64 {
            self.input_fence.get_value()
        }

        fn output_mapped_at(&self, slot: usize) -> Result<*mut c_void, String> {
            if slot == 0 {
                return Ok(self.output_mapped);
            }
            self.output_resources
                .get(slot - 1)
                .map(|output| output.mapped_resource)
                .ok_or_else(|| format!("NVENC output ring slot {} is not registered", slot))
        }

        fn output_fence_ptr_at(&mut self, slot: usize) -> Result<*mut c_void, String> {
            if slot == 0 {
                return Ok(self.output_fence.as_mut_ptr().cast());
            }
            self.output_resources
                .get_mut(slot - 1)
                .map(|output| output.fence.as_mut_ptr().cast())
                .ok_or_else(|| format!("NVENC output ring slot {} is not registered", slot))
        }

        fn output_fence_value_at(&mut self, slot: usize) -> Result<u64, String> {
            if slot == 0 {
                return Ok(self.output_fence.get_value());
            }
            self.output_resources
                .get_mut(slot - 1)
                .map(|output| output.fence.get_value())
                .ok_or_else(|| format!("NVENC output ring slot {} is not registered", slot))
        }

        pub fn output_fence_completed_value(&mut self, slot: usize) -> Result<u64, String> {
            self.output_fence_value_at(slot)
        }

        pub fn submit_surface_at(
            &mut self,
            surface: &D3d12Surface,
            slot: usize,
            frame_index: u32,
            input_fence_value: u64,
            input_time_stamp: u32,
            force_idr: bool,
        ) -> Result<PendingBitstream, String> {
            if self.eos_sent {
                return Err("NVENC encoder has already received EOS".into());
            }
            if let Some(last_frame_index) = self.last_submitted_frame_index {
                if frame_index <= last_frame_index {
                    return Err(format!(
                        "NVENC frame index must increase strictly: previous {}, got {}",
                        last_frame_index, frame_index
                    ));
                }
            }
            if self.input_resources.len() <= slot {
                self.register_surface_at(slot, surface)?;
            }
            let input_mapped = self
                .input_resources
                .get(slot)
                .ok_or_else(|| format!("NVENC input surface slot {} is not registered", slot))?
                .mapped_resource;
            let output_mapped = self.output_mapped_at(slot)?;
            let output_fence_ptr = self.output_fence_ptr_at(slot)?;
            let input_fence =
                make_input_fence_point(self.input_fence.as_mut_ptr().cast(), input_fence_value);
            let output_fence_value = u64::from(frame_index).saturating_add(1);
            let mut output_fence = NV_ENC_FENCE_POINT_D3D12 {
                version: NV_ENC_FENCE_POINT_D3D12_VER,
                pFence: output_fence_ptr,
                signalValue: output_fence_value,
                ..Default::default()
            };
            output_fence.set_bSignal(1);
            let mut input_d3d12 = NV_ENC_INPUT_RESOURCE_D3D12 {
                version: NV_ENC_INPUT_RESOURCE_D3D12_VER,
                pInputBuffer: input_mapped,
                inputFencePoint: input_fence,
                ..Default::default()
            };
            let mut output_d3d12 = NV_ENC_OUTPUT_RESOURCE_D3D12 {
                version: NV_ENC_OUTPUT_RESOURCE_D3D12_VER,
                pOutputBuffer: output_mapped,
                outputFencePoint: output_fence,
                ..Default::default()
            };
            let mut picture = NV_ENC_PIC_PARAMS {
                version: NV_ENC_PIC_PARAMS_VER,
                inputWidth: self.width,
                inputHeight: self.height,
                inputBuffer: (&mut input_d3d12 as *mut NV_ENC_INPUT_RESOURCE_D3D12).cast(),
                outputBitstream: (&mut output_d3d12 as *mut NV_ENC_OUTPUT_RESOURCE_D3D12).cast(),
                bufferFmt: self.buffer_format,
                pictureStruct: NV_ENC_PIC_STRUCT::NV_ENC_PIC_STRUCT_FRAME,
                inputTimeStamp: u64::from(input_time_stamp),
                encodePicFlags: if force_idr {
                    NV_ENC_PIC_FLAGS::NV_ENC_PIC_FLAG_FORCEIDR as u32
                } else {
                    0
                },
                ..Default::default()
            };
            check_status("NvEncEncodePicture", unsafe {
                (ENCODE_API.encode_picture)(self.session, &mut picture)
            })?;
            self.last_submitted_frame_index = Some(frame_index);
            Ok(PendingBitstream {
                slot,
                frame_index,
                output_fence_value,
            })
        }

        pub fn drain_bitstream(&mut self, pending: PendingBitstream) -> Result<Vec<u8>, String> {
            // Host-side wait for the encoder to signal the output fence. In
            // async mode NvEncEncodePicture returns before the encode completes
            // and the driver rejects bWait=1 on NvEncLockBitstream, so poll the
            // D3D12 fence here before locking the bitstream. Spin tightly for
            // the first few million polls: the encode completes in well under a
            // millisecond at this resolution, and yielding on every poll turns a
            // sub-millisecond encode into a full scheduler-quantum stall
            // (~10 ms on Windows).
            let mut wait_iters: u32 = 0;
            while self.output_fence_value_at(pending.slot)? < pending.output_fence_value {
                if wait_iters < 4_000_000 {
                    std::hint::spin_loop();
                } else {
                    std::thread::yield_now();
                }
                wait_iters = wait_iters.saturating_add(1);
                if wait_iters > 20_000_000 {
                    return Err(format!(
                        "NVENC output fence wait timed out for frame {}",
                        pending.output_fence_value
                    ));
                }
            }
            let output_mapped = self.output_mapped_at(pending.slot)?;
            let output_fence_ptr = self.output_fence_ptr_at(pending.slot)?;
            let mut output_fence = NV_ENC_FENCE_POINT_D3D12 {
                version: NV_ENC_FENCE_POINT_D3D12_VER,
                pFence: output_fence_ptr,
                signalValue: pending.output_fence_value,
                ..Default::default()
            };
            output_fence.set_bSignal(1);
            let mut output_d3d12 = NV_ENC_OUTPUT_RESOURCE_D3D12 {
                version: NV_ENC_OUTPUT_RESOURCE_D3D12_VER,
                pOutputBuffer: output_mapped,
                outputFencePoint: output_fence,
                ..Default::default()
            };
            let mut lock = moq_nvenc::sys::nvEncodeAPI::NV_ENC_LOCK_BITSTREAM {
                version: moq_nvenc::sys::nvEncodeAPI::NV_ENC_LOCK_BITSTREAM_VER,
                outputBitstream: (&mut output_d3d12 as *mut NV_ENC_OUTPUT_RESOURCE_D3D12).cast(),
                ..Default::default()
            };
            let locked = check_status("NvEncLockBitstream", unsafe {
                (ENCODE_API.lock_bitstream)(self.session, &mut lock)
            });
            let lock_ok = locked.is_ok();
            let result = if let Err(error) = locked {
                Err(error)
            } else if lock.bitstreamBufferPtr.is_null() {
                Err("NvEncLockBitstream returned a null compressed-bitstream pointer".into())
            } else if u64::from(lock.bitstreamSizeInBytes) > self.output_capacity {
                Err(format!(
                    "NvEncLockBitstream returned {} bytes over {}-byte output capacity",
                    lock.bitstreamSizeInBytes, self.output_capacity
                ))
            } else {
                Ok(unsafe {
                    std::slice::from_raw_parts(
                        lock.bitstreamBufferPtr.cast::<u8>(),
                        lock.bitstreamSizeInBytes as usize,
                    )
                    .to_vec()
                })
            };
            if lock_ok {
                let _ = unsafe {
                    (ENCODE_API.unlock_bitstream)(
                        self.session,
                        (&mut output_d3d12 as *mut NV_ENC_OUTPUT_RESOURCE_D3D12).cast(),
                    )
                };
            }
            if result.is_ok()
                && self.output_fence_value_at(pending.slot)? < pending.output_fence_value
            {
                return Err(format!(
                    "NVENC output fence did not reach frame {}",
                    pending.output_fence_value
                ));
            }
            if result.is_ok() {
                self.last_frame_index = Some(pending.frame_index);
                self.last_output_slot = Some(pending.slot);
            }
            result
        }

        pub fn encode_surface(
            &mut self,
            surface: &D3d12Surface,
            frame_index: u32,
        ) -> Result<Vec<u8>, String> {
            self.encode_surface_at(
                surface,
                0,
                frame_index,
                u64::from(frame_index).saturating_add(1),
            )
        }

        pub fn encode_surface_at(
            &mut self,
            surface: &D3d12Surface,
            slot: usize,
            frame_index: u32,
            input_fence_value: u64,
        ) -> Result<Vec<u8>, String> {
            if self.eos_sent {
                return Err("NVENC encoder has already received EOS".into());
            }
            if self.input_resources.len() <= slot {
                self.register_surface_at(slot, surface)?;
            }
            let input_mapped = self
                .input_resources
                .get(slot)
                .ok_or_else(|| format!("NVENC input surface slot {} is not registered", slot))?
                .mapped_resource;

            let input_fence =
                make_input_fence_point(self.input_fence.as_mut_ptr().cast(), input_fence_value);
            let output_mapped = self.output_mapped_at(slot)?;
            let mut output_fence = NV_ENC_FENCE_POINT_D3D12 {
                version: NV_ENC_FENCE_POINT_D3D12_VER,
                pFence: self.output_fence.as_mut_ptr().cast(),
                signalValue: u64::from(frame_index).saturating_add(1),
                ..Default::default()
            };
            output_fence.set_bSignal(1);
            let mut input_d3d12 = NV_ENC_INPUT_RESOURCE_D3D12 {
                version: NV_ENC_INPUT_RESOURCE_D3D12_VER,
                pInputBuffer: input_mapped,
                inputFencePoint: input_fence,
                ..Default::default()
            };
            let mut output_d3d12 = NV_ENC_OUTPUT_RESOURCE_D3D12 {
                version: NV_ENC_OUTPUT_RESOURCE_D3D12_VER,
                pOutputBuffer: output_mapped,
                outputFencePoint: output_fence,
                ..Default::default()
            };
            let mut picture = NV_ENC_PIC_PARAMS {
                version: NV_ENC_PIC_PARAMS_VER,
                inputWidth: self.width,
                inputHeight: self.height,
                inputBuffer: (&mut input_d3d12 as *mut NV_ENC_INPUT_RESOURCE_D3D12).cast(),
                outputBitstream: (&mut output_d3d12 as *mut NV_ENC_OUTPUT_RESOURCE_D3D12).cast(),
                bufferFmt: self.buffer_format,
                pictureStruct: NV_ENC_PIC_STRUCT::NV_ENC_PIC_STRUCT_FRAME,
                inputTimeStamp: u64::from(frame_index),
                encodePicFlags: if frame_index == 0 {
                    NV_ENC_PIC_FLAGS::NV_ENC_PIC_FLAG_FORCEIDR as u32
                } else {
                    0
                },
                ..Default::default()
            };
            let encode_fn = ENCODE_API.encode_picture;
            if let Err(error) = check_status("NvEncEncodePicture", unsafe {
                encode_fn(self.session, &mut picture)
            }) {
                return Err(error);
            }

            let mut lock = moq_nvenc::sys::nvEncodeAPI::NV_ENC_LOCK_BITSTREAM {
                version: moq_nvenc::sys::nvEncodeAPI::NV_ENC_LOCK_BITSTREAM_VER,
                outputBitstream: (&mut output_d3d12 as *mut NV_ENC_OUTPUT_RESOURCE_D3D12).cast(),
                ..Default::default()
            };
            let lock_fn = ENCODE_API.lock_bitstream;
            let locked = check_status("NvEncLockBitstream", unsafe {
                lock_fn(self.session, &mut lock)
            });
            let lock_ok = locked.is_ok();
            let result = if let Err(error) = locked {
                Err(error)
            } else if lock.bitstreamBufferPtr.is_null() {
                Err("NvEncLockBitstream returned a null compressed-bitstream pointer".into())
            } else if u64::from(lock.bitstreamSizeInBytes) > self.output_capacity {
                Err(format!(
                    "NvEncLockBitstream returned {} bytes over {}-byte output capacity",
                    lock.bitstreamSizeInBytes, self.output_capacity
                ))
            } else {
                Ok(unsafe {
                    std::slice::from_raw_parts(
                        lock.bitstreamBufferPtr.cast::<u8>(),
                        lock.bitstreamSizeInBytes as usize,
                    )
                    .to_vec()
                })
            };
            if lock_ok {
                let _ = unsafe {
                    (ENCODE_API.unlock_bitstream)(
                        self.session,
                        (&mut output_d3d12 as *mut NV_ENC_OUTPUT_RESOURCE_D3D12).cast(),
                    )
                };
            }
            if result.is_ok()
                && self.output_fence.get_value() < u64::from(frame_index).saturating_add(1)
            {
                return Err(format!(
                    "NVENC output fence did not reach frame {}",
                    frame_index + 1
                ));
            }
            if result.is_ok() {
                self.last_frame_index = Some(frame_index);
                self.last_output_slot = Some(slot);
            }
            result
        }

        pub fn flush(&mut self) -> Result<(), String> {
            let Some(last_frame_index) = self.last_frame_index else {
                return Ok(());
            };
            let required = u64::from(last_frame_index).saturating_add(1);
            let output_slot = self.last_output_slot.unwrap_or(0);
            if self.output_fence_value_at(output_slot)? < required {
                return Err(format!(
                    "NVENC flush output fence did not reach frame {} at slot {}",
                    required, output_slot
                ));
            }
            if !self.eos_sent {
                let mut eos = eos_picture_params();
                check_status("NvEncEncodePicture(EOS)", unsafe {
                    (ENCODE_API.encode_picture)(self.session, &mut eos)
                })?;
                self.eos_sent = true;
            }
            Ok(())
        }

        fn unmap(&self, mapped: *mut c_void) {
            let _ = unsafe { (ENCODE_API.unmap_input_resource)(self.session, mapped) };
        }

        fn unregister(&self, registered: *mut c_void) {
            let _ = unsafe { (ENCODE_API.unregister_resource)(self.session, registered) };
        }
    }

    impl Drop for D3d12NvencEncoder {
        fn drop(&mut self) {
            if self.session.is_null() {
                return;
            }
            for input in self.input_resources.drain(..) {
                if !input.mapped_resource.is_null() {
                    let _ = unsafe {
                        (ENCODE_API.unmap_input_resource)(self.session, input.mapped_resource)
                    };
                }
                if !input.registered_resource.is_null() {
                    let _ = unsafe {
                        (ENCODE_API.unregister_resource)(self.session, input.registered_resource)
                    };
                }
            }
            for output in self.output_resources.drain(..) {
                if !output.mapped_resource.is_null() {
                    let _ = unsafe {
                        (ENCODE_API.unmap_input_resource)(self.session, output.mapped_resource)
                    };
                }
                if !output.registered_resource.is_null() {
                    let _ = unsafe {
                        (ENCODE_API.unregister_resource)(self.session, output.registered_resource)
                    };
                }
            }
            if !self.output_mapped.is_null() {
                let _ =
                    unsafe { (ENCODE_API.unmap_input_resource)(self.session, self.output_mapped) };
                self.output_mapped = null_mut();
            }
            if !self.output_registered.is_null() {
                let _ = unsafe {
                    (ENCODE_API.unregister_resource)(self.session, self.output_registered)
                };
                self.output_registered = null_mut();
            }
            let _ = unsafe { (ENCODE_API.destroy_encoder)(self.session) };
            self.session = null_mut();
        }
    }

    pub fn run_probe(config: &super::DirectEncoderProbeConfig) -> Result<(), String> {
        let instance = wgpu::Instance::default();
        let selected = crate::adapter::select_with_backends(&instance, wgpu::Backends::DX12)?;
        let adapter_name = selected.info.name.clone();
        let backend = format!("{:?}", selected.info.backend).to_ascii_lowercase();
        let (device, queue) = pollster::block_on(
            selected
                .adapter
                .request_device(&wgpu::DeviceDescriptor::default(), None),
        )
        .map_err(|error| format!("D3D12 probe device: {error}"))?;

        let mut encoder =
            D3d12NvencEncoder::create(&device, config.width, config.height, config.fps)?;
        let surface = D3d12Surface::create(&device, config.width, config.height)?;
        let view = surface
            .texture
            .create_view(&wgpu::TextureViewDescriptor::default());
        let mut command_encoder = device.create_command_encoder(&wgpu::CommandEncoderDescriptor {
            label: Some("native-nvenc-probe-dispatch"),
        });
        {
            let _pass = command_encoder.begin_render_pass(&wgpu::RenderPassDescriptor {
                label: Some("native-nvenc-probe-clear"),
                color_attachments: &[Some(wgpu::RenderPassColorAttachment {
                    view: &view,
                    resolve_target: None,
                    ops: wgpu::Operations {
                        load: wgpu::LoadOp::Clear(wgpu::Color {
                            r: 0.25,
                            g: 0.5,
                            b: 0.75,
                            a: 1.0,
                        }),
                        store: wgpu::StoreOp::Store,
                    },
                })],
                depth_stencil_attachment: None,
                occlusion_query_set: None,
                timestamp_writes: None,
            });
        }
        queue.submit(Some(command_encoder.finish()));
        encoder.signal_input_fence(&device, 1)?;
        // This waits for GPU completion but never maps/copies pixel data back.
        device.poll(wgpu::Maintain::Wait);

        let contract = surface.contract(1);
        contract.validate()?;
        let bitstream = encoder.encode_surface(&surface, 0)?;
        if bitstream.is_empty() {
            return Err("NVENC returned an empty bitstream".into());
        }
        if let Some(parent) = config
            .output
            .parent()
            .filter(|path| !path.as_os_str().is_empty())
        {
            std::fs::create_dir_all(parent)
                .map_err(|error| format!("create NVENC probe directory: {error}"))?;
        }
        std::fs::write(&config.output, &bitstream)
            .map_err(|error| format!("write NVENC probe bitstream: {error}"))?;
        eprintln!(
            "direct_nvenc_probe=pass adapter={} backend={} size={}x{} fps={} bytes={} pixel_readback_bytes=0 output={}",
            adapter_name,
            backend,
            config.width,
            config.height,
            config.fps,
            bitstream.len(),
            config.output.display()
        );
        Ok(())
    }

    fn check_status(stage: &str, status: NVENCSTATUS) -> Result<(), String> {
        if status == NVENCSTATUS::NV_ENC_SUCCESS {
            Ok(())
        } else {
            Err(format!("{stage} failed: {status:?}"))
        }
    }

    fn make_input_fence_point(fence: *mut c_void, wait_value: u64) -> NV_ENC_FENCE_POINT_D3D12 {
        let mut point = NV_ENC_FENCE_POINT_D3D12 {
            version: NV_ENC_FENCE_POINT_D3D12_VER,
            pFence: fence,
            waitValue: wait_value,
            ..Default::default()
        };
        point.set_bWait(1);
        point
    }

    fn eos_picture_params() -> NV_ENC_PIC_PARAMS {
        NV_ENC_PIC_PARAMS {
            version: NV_ENC_PIC_PARAMS_VER,
            encodePicFlags: NV_ENC_PIC_FLAGS::NV_ENC_PIC_FLAG_EOS as u32,
            ..Default::default()
        }
    }

    unsafe fn create_fence(device: &d3d12::Device) -> Result<Fence, String> {
        let mut raw_fence: *mut winapi::um::d3d12::ID3D12Fence = null_mut();
        let result = device.CreateFence(
            0,
            D3D12_FENCE_FLAG_NONE,
            &winapi::um::d3d12::ID3D12Fence::uuidof(),
            &mut raw_fence as *mut _ as *mut *mut c_void,
        );
        if !SUCCEEDED(result) || raw_fence.is_null() {
            return Err(format!(
                "CreateFence(NVENC synchronization) failed: HRESULT 0x{result:08x}"
            ));
        }
        Ok(com_ptr_from_owned(raw_fence))
    }

    unsafe fn create_rgba_resource(
        device: &d3d12::Device,
        width: u32,
        height: u32,
    ) -> Result<Resource, String> {
        let heap_properties = D3D12_HEAP_PROPERTIES {
            Type: D3D12_HEAP_TYPE_DEFAULT,
            CPUPageProperty: winapi::um::d3d12::D3D12_CPU_PAGE_PROPERTY_UNKNOWN,
            MemoryPoolPreference: winapi::um::d3d12::D3D12_MEMORY_POOL_UNKNOWN,
            CreationNodeMask: 0,
            VisibleNodeMask: 0,
        };
        let resource_desc = D3D12_RESOURCE_DESC {
            Dimension: D3D12_RESOURCE_DIMENSION_TEXTURE2D,
            Alignment: 0,
            Width: u64::from(width),
            Height: height,
            DepthOrArraySize: 1,
            MipLevels: 1,
            Format: DXGI_FORMAT_B8G8R8A8_UNORM,
            SampleDesc: DXGI_SAMPLE_DESC {
                Count: 1,
                Quality: 0,
            },
            Layout: D3D12_TEXTURE_LAYOUT_UNKNOWN,
            Flags: D3D12_RESOURCE_FLAG_ALLOW_RENDER_TARGET,
        };
        let mut raw_resource: *mut winapi::um::d3d12::ID3D12Resource = null_mut();
        let result = device.CreateCommittedResource(
            &heap_properties,
            D3D12_HEAP_FLAG_NONE,
            &resource_desc,
            D3D12_RESOURCE_STATE_RENDER_TARGET,
            null(),
            &winapi::um::d3d12::ID3D12Resource::uuidof(),
            &mut raw_resource as *mut _ as *mut *mut c_void,
        );
        if !SUCCEEDED(result) || raw_resource.is_null() {
            return Err(format!(
                "CreateCommittedResource(B8G8R8A8_UNORM) failed: HRESULT 0x{result:08x}"
            ));
        }
        Ok(com_ptr_from_owned(raw_resource))
    }

    unsafe fn create_bitstream_resource(
        device: &d3d12::Device,
        capacity: u64,
    ) -> Result<Resource, String> {
        let heap_properties = D3D12_HEAP_PROPERTIES {
            Type: D3D12_HEAP_TYPE_READBACK,
            CPUPageProperty: winapi::um::d3d12::D3D12_CPU_PAGE_PROPERTY_UNKNOWN,
            MemoryPoolPreference: winapi::um::d3d12::D3D12_MEMORY_POOL_UNKNOWN,
            CreationNodeMask: 0,
            VisibleNodeMask: 0,
        };
        let resource_desc = D3D12_RESOURCE_DESC {
            Dimension: D3D12_RESOURCE_DIMENSION_BUFFER,
            Alignment: 0,
            Width: capacity,
            Height: 1,
            DepthOrArraySize: 1,
            MipLevels: 1,
            Format: DXGI_FORMAT_UNKNOWN,
            SampleDesc: DXGI_SAMPLE_DESC {
                Count: 1,
                Quality: 0,
            },
            Layout: D3D12_TEXTURE_LAYOUT_ROW_MAJOR,
            Flags: D3D12_RESOURCE_FLAG_NONE,
        };
        let mut raw_resource: *mut winapi::um::d3d12::ID3D12Resource = null_mut();
        let result = device.CreateCommittedResource(
            &heap_properties,
            D3D12_HEAP_FLAG_NONE,
            &resource_desc,
            D3D12_RESOURCE_STATE_COPY_DEST,
            null(),
            &winapi::um::d3d12::ID3D12Resource::uuidof(),
            &mut raw_resource as *mut _ as *mut *mut c_void,
        );
        if !SUCCEEDED(result) || raw_resource.is_null() {
            return Err(format!(
                "CreateCommittedResource(NVENC readback output) failed: HRESULT 0x{result:08x}"
            ));
        }
        Ok(com_ptr_from_owned(raw_resource))
    }

    #[cfg(test)]
    mod fence_tests {
        use super::*;

        #[test]
        fn input_fence_point_waits_for_the_submitted_frame() {
            let fence = 0x1234usize as *mut c_void;
            let point = make_input_fence_point(fence, 7);
            assert_eq!(point.pFence, fence);
            assert_eq!(point.waitValue, 7);
            assert_eq!(point.signalValue, 0);
            assert_eq!(point.bWait(), 1);
            assert_eq!(point.bSignal(), 0);
        }

        #[test]
        fn eos_picture_requests_encoder_drain() {
            let params = eos_picture_params();
            assert_ne!(
                params.encodePicFlags & NV_ENC_PIC_FLAGS::NV_ENC_PIC_FLAG_EOS as u32,
                0
            );
            assert!(params.inputBuffer.is_null());
            assert!(params.outputBitstream.is_null());
        }
    }
}

#[cfg(windows)]
pub use windows_impl::{
    nvenc_runtime_available, run_probe, D3d12NvencEncoder, D3d12Surface, PendingBitstream,
};

#[cfg(not(windows))]
pub fn nvenc_runtime_available() -> bool {
    false
}

fn constant_qp_values(parity_lossless: bool) -> (u32, u32, u32) {
    if parity_lossless {
        (0, 0, 0)
    } else {
        (28, 31, 25)
    }
}

#[cfg(not(windows))]
pub struct D3d12Surface;

#[cfg(not(windows))]
pub fn run_probe(_config: &DirectEncoderProbeConfig) -> Result<(), String> {
    Err("direct D3D12/NVENC probe is only available on Windows".into())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parity_lossless_qp_override_is_explicit_and_default_is_unchanged() {
        assert_eq!(constant_qp_values(false), (28, 31, 25));
        assert_eq!(constant_qp_values(true), (0, 0, 0));
    }

    #[test]
    fn direct_encoder_contract_requires_d3d12_and_zero_pixel_readback() {
        let contract = DirectEncoderContract::new(640, 360, "vulkan", 1, 0);
        assert_eq!(
            contract.validate(),
            Err("direct NVENC requires the D3D12 backend".into())
        );

        let contract = DirectEncoderContract::new(640, 360, "dx12", 1, 345_600);
        assert_eq!(
            contract.validate(),
            Err("direct NVENC cannot accept a pixel readback".into())
        );
    }

    #[test]
    fn direct_encoder_contract_accepts_encoded_bitstream_readback_only() {
        let contract = DirectEncoderContract::new(640, 360, "dx12", 1, 0);
        assert_eq!(contract.validate(), Ok(()));
    }

    #[test]
    fn parses_direct_encoder_probe_arguments() {
        let args = vec![
            "--width".into(),
            "1280".into(),
            "--height".into(),
            "720".into(),
            "--fps".into(),
            "30".into(),
            "--output".into(),
            "probe.h264".into(),
        ];
        let config = parse_probe_args(&args).expect("valid probe args");
        assert_eq!(config.width, 1280);
        assert_eq!(config.height, 720);
        assert_eq!(config.fps, 30);
        assert_eq!(config.output, std::path::PathBuf::from("probe.h264"));
    }

    #[test]
    fn direct_encoder_probe_requires_output_and_positive_dimensions() {
        assert_eq!(parse_probe_args(&[]), Err("--output is required".into()));
        let args = vec![
            "--width".into(),
            "0".into(),
            "--output".into(),
            "probe.h264".into(),
        ];
        assert_eq!(
            parse_probe_args(&args),
            Err("NVENC probe dimensions and fps must be non-zero".into())
        );
    }
}
