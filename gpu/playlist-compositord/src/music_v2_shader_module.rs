#![allow(dead_code)]

use super::music_v2_pass_graph::{Pass, CANONICAL_ORDER};

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ShaderBindingKind {
    StorageRead,
    Uniform,
    /// Read-only RGBA8 texture carrying the rasterized font atlas.
    SampledTextureRgba8,
    /// Filtering sampler paired with the glyph atlas texture.
    Sampler,
    /// Portable RGBA8 storage texture carrying a logical luma plane in R.
    StorageTextureRgba8,
    /// Portable RGBA8 storage texture carrying logical chroma planes in R/G.
    StorageTextureRgba8Chroma,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct ShaderBinding {
    pub index: u32,
    pub name: &'static str,
    pub kind: ShaderBindingKind,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct ShaderModuleDescriptor {
    pub version: u16,
    pub entry_point: &'static str,
    pub workgroup_size: (u32, u32, u32),
    pub bindings: &'static [ShaderBinding],
    pub passes: &'static [Pass],
    pub gpu_owned_output: bool,
    pub production_ready: bool,
}

pub trait ShaderModule {
    fn descriptor(&self) -> ShaderModuleDescriptor;
    fn wgsl(&self) -> &'static str;
}

pub fn validate_descriptor(descriptor: ShaderModuleDescriptor) -> Result<(), String> {
    if descriptor.version == 0 {
        return Err("shader module version must be non-zero".into());
    }
    if descriptor.entry_point.is_empty() {
        return Err("shader entry point must not be empty".into());
    }
    if descriptor.workgroup_size.0 == 0
        || descriptor.workgroup_size.1 == 0
        || descriptor.workgroup_size.2 == 0
    {
        return Err("shader workgroup dimensions must be non-zero".into());
    }
    if !descriptor.gpu_owned_output {
        return Err("shader output must remain GPU-owned".into());
    }
    if descriptor.passes != CANONICAL_ORDER.as_slice() {
        return Err("shader pass order must match CANONICAL_ORDER".into());
    }
    if descriptor.bindings.len() != 10 {
        return Err("shader binding manifest must contain ten bindings".into());
    }

    for (position, binding) in descriptor.bindings.iter().enumerate() {
        if binding.index != position as u32 {
            return Err(format!(
                "shader binding {} is not at index {}",
                binding.name, position
            ));
        }
        if binding.name.is_empty() {
            return Err(format!("shader binding {} has no name", binding.index));
        }
        if descriptor
            .bindings
            .iter()
            .skip(position + 1)
            .any(|other| other.index == binding.index)
        {
            return Err(format!("duplicate shader binding index {}", binding.index));
        }
    }

    let expected = [
        (0, ShaderBindingKind::StorageRead),
        (1, ShaderBindingKind::Uniform),
        (2, ShaderBindingKind::StorageRead),
        (3, ShaderBindingKind::SampledTextureRgba8),
        (4, ShaderBindingKind::StorageRead),
        (5, ShaderBindingKind::Sampler),
        (6, ShaderBindingKind::SampledTextureRgba8),
        (7, ShaderBindingKind::StorageTextureRgba8),
        (8, ShaderBindingKind::StorageTextureRgba8Chroma),
        (9, ShaderBindingKind::StorageTextureRgba8),
    ];
    for (index, kind) in expected {
        let actual = descriptor
            .bindings
            .iter()
            .find(|binding| binding.index == index)
            .map(|binding| binding.kind);
        if actual != Some(kind) {
            return Err(format!("shader binding {} has an incompatible kind", index));
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    const TEST_BINDINGS: [ShaderBinding; 10] = [
        ShaderBinding {
            index: 0,
            name: "pcm_input",
            kind: ShaderBindingKind::StorageRead,
        },
        ShaderBinding {
            index: 1,
            name: "scene",
            kind: ShaderBindingKind::Uniform,
        },
        ShaderBinding {
            index: 2,
            name: "waveform_q16",
            kind: ShaderBindingKind::StorageRead,
        },
        ShaderBinding {
            index: 3,
            name: "glyph_atlas",
            kind: ShaderBindingKind::SampledTextureRgba8,
        },
        ShaderBinding {
            index: 4,
            name: "glyph_metrics",
            kind: ShaderBindingKind::StorageRead,
        },
        ShaderBinding {
            index: 5,
            name: "glyph_sampler",
            kind: ShaderBindingKind::Sampler,
        },
        ShaderBinding {
            index: 6,
            name: "artwork_texture",
            kind: ShaderBindingKind::SampledTextureRgba8,
        },
        ShaderBinding {
            index: 7,
            name: "luma_output",
            kind: ShaderBindingKind::StorageTextureRgba8,
        },
        ShaderBinding {
            index: 8,
            name: "chroma_output",
            kind: ShaderBindingKind::StorageTextureRgba8Chroma,
        },
        ShaderBinding {
            index: 9,
            name: "rgb_output",
            kind: ShaderBindingKind::StorageTextureRgba8,
        },
    ];

    fn valid_descriptor() -> ShaderModuleDescriptor {
        ShaderModuleDescriptor {
            version: 1,
            entry_point: "render_draft",
            workgroup_size: (8, 8, 1),
            bindings: &TEST_BINDINGS,
            passes: &CANONICAL_ORDER,
            gpu_owned_output: true,
            production_ready: false,
        }
    }

    #[test]
    fn validates_gpu_owned_draft_descriptor() {
        assert!(validate_descriptor(valid_descriptor()).is_ok());
    }

    #[test]
    fn rejects_cpu_owned_output_descriptor() {
        let descriptor = ShaderModuleDescriptor {
            gpu_owned_output: false,
            ..valid_descriptor()
        };
        assert!(validate_descriptor(descriptor).is_err());
    }

    #[test]
    fn rejects_noncanonical_pass_order() {
        let descriptor = ShaderModuleDescriptor {
            passes: &[],
            ..valid_descriptor()
        };
        assert!(validate_descriptor(descriptor).is_err());
    }
}
