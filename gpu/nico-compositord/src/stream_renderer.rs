//! Budgeted uploads for verified NCT2 assets.
//!
//! This stage uploads each parser asset as soon as it is available. The CPU
//! pixel credit remains attached to the event until the bytes have been copied
//! into an explicitly-budgeted mapped staging buffer and that buffer is
//! unmapped. The staging allocation and its GPU credit remain alive until the
//! corresponding queue completion callback is observed.

use std::collections::HashMap;
use std::fmt;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};

use crate::renderer::Renderer;
use crate::stream_budget::ParserEvent;
use crate::stream_protocol::{IncrementalEvent, StreamAsset, StreamEvent};

pub const GPU_BUDGET_BYTES: usize = 768 * 1024 * 1024;
const COPY_BYTES_PER_ROW_ALIGNMENT: usize = 256;

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct GpuBudgetError;

impl fmt::Display for GpuBudgetError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("stream GPU allocation exceeds the 768 MiB budget")
    }
}

impl std::error::Error for GpuBudgetError {}

#[derive(Debug)]
struct GpuBudgetState {
    used: Mutex<usize>,
}

#[derive(Debug, Clone)]
pub struct GpuBudget {
    state: Arc<GpuBudgetState>,
}

impl Default for GpuBudget {
    fn default() -> Self {
        Self::new()
    }
}

impl GpuBudget {
    pub fn new() -> Self {
        Self {
            state: Arc::new(GpuBudgetState {
                used: Mutex::new(0),
            }),
        }
    }

    pub fn limit_bytes(&self) -> usize {
        GPU_BUDGET_BYTES
    }

    pub fn used_bytes(&self) -> usize {
        *self.state.used.lock().expect("GPU budget mutex poisoned")
    }

    pub fn try_reserve(&self, bytes: usize) -> Result<GpuCredit, GpuBudgetError> {
        let mut used = self.state.used.lock().expect("GPU budget mutex poisoned");
        if bytes > GPU_BUDGET_BYTES.saturating_sub(*used) {
            return Err(GpuBudgetError);
        }
        *used += bytes;
        Ok(GpuCredit {
            state: Some(Arc::clone(&self.state)),
            bytes,
        })
    }
}

/// A move-only reservation returned when the corresponding allocation drops.
#[derive(Debug)]
pub struct GpuCredit {
    state: Option<Arc<GpuBudgetState>>,
    bytes: usize,
}

impl GpuCredit {
    pub fn bytes(&self) -> usize {
        self.bytes
    }
}

impl Drop for GpuCredit {
    fn drop(&mut self) {
        if let Some(state) = self.state.take() {
            let mut used = state.used.lock().expect("GPU budget mutex poisoned");
            *used = used
                .checked_sub(self.bytes)
                .expect("GPU credit returned more than once");
        }
    }
}

#[derive(Debug, Clone, Default)]
pub struct UploadCancellation {
    cancelled: Arc<AtomicBool>,
}

impl UploadCancellation {
    pub fn new() -> Self {
        Self::default()
    }

    pub fn cancel(&self) {
        self.cancelled.store(true, Ordering::Release);
    }

    pub fn is_cancelled(&self) -> bool {
        self.cancelled.load(Ordering::Acquire)
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum UploadDeferredReason {
    CreditsUnavailable,
    StagingBusy,
    Cancelled,
    NotAsset,
}

pub enum UploadAttempt<E> {
    Submitted {
        asset_id: u32,
        token: u64,
    },
    Deferred {
        reason: UploadDeferredReason,
        event: E,
    },
}

/// Operations needed to create/copy/submit one texture upload.
///
/// Implementations must ensure `copy_rgba_to_staging` finishes its final CPU
/// copy and unmaps staging before returning success. Submission completion is
/// reported later by token through `poll_completed`.
pub trait UploadBackend {
    type Texture: Send + Sync + 'static;
    type Staging: Send + Sync + 'static;

    fn validate_texture_extent(&self, width: u32, height: u32) -> Result<(), String>;
    fn validate_asset_registration(&self, _asset_id: u32) -> Result<(), String> {
        Ok(())
    }
    fn create_texture(&mut self, width: u32, height: u32) -> Result<Self::Texture, String>;
    fn create_staging(&mut self, bytes: usize) -> Result<Self::Staging, String>;
    fn copy_rgba_to_staging(
        &mut self,
        staging: &mut Self::Staging,
        rgba: &[u8],
        width: u32,
        height: u32,
        padded_bytes_per_row: u32,
    ) -> Result<(), String>;
    /// Submit the upload and retain `retirement` until this submission's
    /// matching completion notification. The backend must not drop it merely
    /// because its caller or uploader is dropped.
    fn submit_upload(
        &mut self,
        staging: &Self::Staging,
        texture: &Self::Texture,
        padded_bytes_per_row: u32,
        retirement: UploadRetirement,
    ) -> Result<u64, String>;
    /// Register the resident texture for Draw resolution after successful
    /// submission. ID validity is checked before allocation by
    /// `validate_asset_registration`; this commit hook must not fail.
    fn register_asset(
        &mut self,
        asset_id: u32,
        width: u32,
        height: u32,
        texture: &Self::Texture,
        owner: Arc<dyn Send + Sync>,
    );
    fn release_scene(&mut self) {}
    fn poll_completed(&mut self) -> Result<Vec<u64>, String>;
}

trait UploadEvent {
    fn asset(&self) -> Option<&StreamAsset>;
}

impl UploadEvent for StreamEvent {
    fn asset(&self) -> Option<&StreamAsset> {
        match self {
            Self::Asset(asset) => Some(asset),
            Self::Complete(_) => None,
        }
    }
}

impl UploadEvent for IncrementalEvent {
    fn asset(&self) -> Option<&StreamAsset> {
        match self {
            Self::Asset(asset) => Some(asset),
            _ => None,
        }
    }
}

/// Opaque keep-alive transferred to the backend's completion boundary.
/// Dropping it releases the captured allocations and their credits.
pub struct UploadRetirement {
    _owner: Box<dyn Send>,
}

impl UploadRetirement {
    fn new<T: Send + 'static>(owner: T) -> Self {
        Self {
            _owner: Box::new(owner),
        }
    }
}

struct Resident<T> {
    _texture: T,
    _credit: GpuCredit,
}

struct RetainedUpload<S, T> {
    _staging: S,
    _staging_credit: GpuCredit,
    _resident: Arc<Resident<T>>,
}

/// Owns resident textures and at most one in-flight padded staging buffer.
/// Resident textures live until `release_scene`; staging lives until its queue
/// completion token is observed.
pub struct StreamAssetUploader<B: UploadBackend> {
    backend: B,
    budget: GpuBudget,
    residents: HashMap<u32, Arc<Resident<B::Texture>>>,
    pending: Option<u64>,
}

impl<B: UploadBackend> StreamAssetUploader<B> {
    pub fn new(backend: B, budget: GpuBudget) -> Self {
        Self {
            backend,
            budget,
            residents: HashMap::new(),
            pending: None,
        }
    }

    pub fn budget_used_bytes(&self) -> usize {
        self.budget.used_bytes()
    }

    pub fn resident_asset_count(&self) -> usize {
        self.residents.len()
    }

    pub(crate) fn has_pending_upload(&self) -> bool {
        self.pending.is_some()
    }

    pub(crate) fn backend_mut(&mut self) -> &mut B {
        &mut self.backend
    }

    /// Upload a verified Asset event without waiting for Complete or EOF.
    /// Deferred attempts return the original event with its CPU pixel credit.
    pub fn try_upload(
        &mut self,
        event: ParserEvent<StreamEvent>,
        cancellation: &UploadCancellation,
    ) -> Result<UploadAttempt<ParserEvent<StreamEvent>>, String> {
        self.try_upload_event(event, cancellation)
    }

    /// Upload a scheduler-driven incremental Asset event and register its
    /// resident texture for subsequent Draw resolution before returning.
    pub fn try_upload_incremental(
        &mut self,
        event: ParserEvent<IncrementalEvent>,
        cancellation: &UploadCancellation,
    ) -> Result<UploadAttempt<ParserEvent<IncrementalEvent>>, String> {
        self.try_upload_event(event, cancellation)
    }

    fn try_upload_event<E: UploadEvent>(
        &mut self,
        event: ParserEvent<E>,
        cancellation: &UploadCancellation,
    ) -> Result<UploadAttempt<ParserEvent<E>>, String> {
        if cancellation.is_cancelled() {
            return Ok(UploadAttempt::Deferred {
                reason: UploadDeferredReason::Cancelled,
                event,
            });
        }
        let Some(asset) = event.payload.get().asset() else {
            return Ok(UploadAttempt::Deferred {
                reason: UploadDeferredReason::NotAsset,
                event,
            });
        };
        let descriptor = asset.descriptor();
        let asset_id = descriptor.id;
        let width = descriptor.width;
        let height = descriptor.height;
        let expected_len = (width as usize)
            .checked_mul(height as usize)
            .and_then(|pixels| pixels.checked_mul(4))
            .ok_or_else(|| "verified asset dimensions overflow usize".to_owned())?;
        if width == 0
            || height == 0
            || width > 16_384
            || height > 16_384
            || expected_len > 128 * 1024 * 1024
            || u64::try_from(expected_len).ok() != Some(descriptor.raw_len)
            || asset.rgba().len() != expected_len
        {
            return Err(format!(
                "verified asset {asset_id} has inconsistent dimensions"
            ));
        }
        if self.residents.contains_key(&asset_id) {
            return Err(format!("duplicate stream asset id {asset_id}"));
        }
        self.backend.validate_asset_registration(asset_id)?;
        if self.pending.is_some() {
            return Ok(UploadAttempt::Deferred {
                reason: UploadDeferredReason::StagingBusy,
                event,
            });
        }
        self.backend.validate_texture_extent(width, height)?;
        let row_bytes = (width as usize)
            .checked_mul(4)
            .ok_or_else(|| "verified asset row size overflow".to_owned())?;
        let padded_bytes_per_row = row_bytes
            .checked_add(COPY_BYTES_PER_ROW_ALIGNMENT - 1)
            .map(|bytes| bytes / COPY_BYTES_PER_ROW_ALIGNMENT * COPY_BYTES_PER_ROW_ALIGNMENT)
            .ok_or_else(|| "padded asset row size overflow".to_owned())?;
        let staging_bytes = padded_bytes_per_row
            .checked_mul(height as usize)
            .ok_or_else(|| "padded asset staging size overflow".to_owned())?;
        let resident_bytes = expected_len;
        let Some(resident_credit) = self.budget.try_reserve(resident_bytes).ok() else {
            return Ok(UploadAttempt::Deferred {
                reason: UploadDeferredReason::CreditsUnavailable,
                event,
            });
        };
        let Some(staging_credit) = self.budget.try_reserve(staging_bytes).ok() else {
            drop(resident_credit);
            return Ok(UploadAttempt::Deferred {
                reason: UploadDeferredReason::CreditsUnavailable,
                event,
            });
        };
        if cancellation.is_cancelled() {
            drop((resident_credit, staging_credit));
            return Ok(UploadAttempt::Deferred {
                reason: UploadDeferredReason::Cancelled,
                event,
            });
        }

        // Both credits exist before either GPU allocation is requested.
        let texture = self.backend.create_texture(width, height)?;
        let mut staging = self.backend.create_staging(staging_bytes)?;
        let asset = event
            .payload
            .get()
            .asset()
            .expect("verified asset event is immutable");
        self.backend.copy_rgba_to_staging(
            &mut staging,
            asset.rgba(),
            width,
            height,
            u32::try_from(padded_bytes_per_row)
                .map_err(|_| "padded asset row size exceeds u32".to_owned())?,
        )?;
        if cancellation.is_cancelled() {
            drop((texture, staging, resident_credit, staging_credit));
            return Ok(UploadAttempt::Deferred {
                reason: UploadDeferredReason::Cancelled,
                event,
            });
        }

        // The last CPU reference is released only after copy and unmap succeed.
        drop(event);
        let resident = Arc::new(Resident {
            _texture: texture,
            _credit: resident_credit,
        });
        let retained = Arc::new(RetainedUpload {
            _staging: staging,
            _staging_credit: staging_credit,
            _resident: Arc::clone(&resident),
        });
        let token = self.backend.submit_upload(
            &retained._staging,
            &resident._texture,
            u32::try_from(padded_bytes_per_row)
                .map_err(|_| "padded asset row size exceeds u32".to_owned())?,
            UploadRetirement::new(Arc::clone(&retained)),
        )?;
        let owner: Arc<dyn Send + Sync> = resident.clone();
        self.backend
            .register_asset(asset_id, width, height, &resident._texture, owner);
        self.residents.insert(asset_id, resident);
        self.pending = Some(token);
        Ok(UploadAttempt::Submitted { asset_id, token })
    }

    /// Poll the backend and release staging only for completed submissions.
    pub fn poll_completions(&mut self) -> Result<bool, String> {
        let Some(token) = self.pending else {
            let _ = self.backend.poll_completed()?;
            return Ok(false);
        };
        let completed = self.backend.poll_completed()?;
        if completed.contains(&token) {
            self.pending = None;
            Ok(true)
        } else {
            Ok(false)
        }
    }

    /// Release all textures after the caller has ended this scene. Uploads
    /// still in flight must first be observed complete.
    pub fn release_scene(&mut self) -> Result<(), String> {
        if self.pending.is_some() {
            return Err("cannot release stream scene with upload in flight".into());
        }
        self.residents.clear();
        self.backend.release_scene();
        Ok(())
    }
}

/// Production WGPU implementation. The Renderer remains the only owner that
/// submits upload work and drives the matching completion callbacks.
pub struct WgpuUploadBackend<'a> {
    renderer: &'a mut Renderer,
}

impl<'a> WgpuUploadBackend<'a> {
    pub fn new(renderer: &'a mut Renderer) -> Self {
        Self { renderer }
    }

    pub(crate) fn renderer_mut(&mut self) -> &mut Renderer {
        self.renderer
    }
}

impl UploadBackend for WgpuUploadBackend<'_> {
    type Texture = wgpu::Texture;
    type Staging = wgpu::Buffer;

    fn validate_texture_extent(&self, width: u32, height: u32) -> Result<(), String> {
        self.renderer
            .validate_stream_texture_extent(width, height)
            .map_err(|error| error.to_string())
    }

    fn validate_asset_registration(&self, asset_id: u32) -> Result<(), String> {
        self.renderer
            .validate_stream_asset_registration(asset_id)
            .map_err(|error| error.to_string())
    }

    fn create_texture(&mut self, width: u32, height: u32) -> Result<Self::Texture, String> {
        self.renderer
            .create_stream_texture(width, height)
            .map_err(|error| error.to_string())
    }

    fn create_staging(&mut self, bytes: usize) -> Result<Self::Staging, String> {
        self.renderer
            .create_stream_staging(bytes as u64)
            .map_err(|error| error.to_string())
    }

    fn copy_rgba_to_staging(
        &mut self,
        staging: &mut Self::Staging,
        rgba: &[u8],
        width: u32,
        height: u32,
        padded_bytes_per_row: u32,
    ) -> Result<(), String> {
        let row_bytes = width as usize * 4;
        let padded = padded_bytes_per_row as usize;
        if rgba.len() != row_bytes.saturating_mul(height as usize)
            || padded < row_bytes
            || padded % COPY_BYTES_PER_ROW_ALIGNMENT != 0
            || padded
                .checked_mul(height as usize)
                .is_none_or(|bytes| bytes as u64 > staging.size())
        {
            return Err("invalid verified asset upload layout".into());
        }
        let mut mapped = staging.slice(..).get_mapped_range_mut();
        for row in 0..height as usize {
            let source_start = row * row_bytes;
            let target_start = row * padded;
            mapped[target_start..target_start + row_bytes]
                .copy_from_slice(&rgba[source_start..source_start + row_bytes]);
        }
        drop(mapped);
        staging.unmap();
        Ok(())
    }

    fn submit_upload(
        &mut self,
        staging: &Self::Staging,
        texture: &Self::Texture,
        padded_bytes_per_row: u32,
        retirement: UploadRetirement,
    ) -> Result<u64, String> {
        self.renderer
            .submit_stream_upload(staging, texture, padded_bytes_per_row, retirement)
            .map_err(|error| error.to_string())
    }

    fn register_asset(
        &mut self,
        asset_id: u32,
        _width: u32,
        _height: u32,
        texture: &Self::Texture,
        owner: Arc<dyn Send + Sync>,
    ) {
        self.renderer
            .register_stream_asset(asset_id, texture, owner);
    }

    fn release_scene(&mut self) {
        self.renderer.release_stream_assets();
    }

    fn poll_completed(&mut self) -> Result<Vec<u64>, String> {
        Ok(self.renderer.poll_stream_upload_completions())
    }
}
