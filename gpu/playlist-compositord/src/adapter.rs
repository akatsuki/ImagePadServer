use wgpu::{Adapter, AdapterInfo, DeviceType, Instance};

#[derive(Debug)]
pub struct Selection {
    pub adapter: Adapter,
    pub info: AdapterInfo,
}

/// Select a real hardware adapter. Discrete GPUs are preferred, while
/// integrated GPUs are accepted. Software/CPU adapters are never selected.
pub fn select(instance: &Instance) -> Result<Selection, String> {
    let preferred = std::env::var("IMAGEPAD_GPU_ADAPTER").ok();
    let mut integrated: Option<Selection> = None;
    let mut preferred_match: Option<Selection> = None;
    for adapter in instance.enumerate_adapters(wgpu::Backends::all()) {
        let info = adapter.get_info();
        let is_hardware = matches!(
            info.device_type,
            DeviceType::DiscreteGpu | DeviceType::IntegratedGpu
        );
        if is_hardware
            && preferred
                .as_deref()
                .is_some_and(|needle| info.name.to_lowercase().contains(&needle.to_lowercase()))
        {
            preferred_match = Some(Selection { adapter, info });
            continue;
        }
        match info.device_type {
            DeviceType::DiscreteGpu if preferred.is_none() => {
                return Ok(Selection { adapter, info })
            }
            DeviceType::DiscreteGpu => {}
            DeviceType::IntegratedGpu if integrated.is_none() => {
                integrated = Some(Selection { adapter, info });
            }
            _ => {}
        }
    }
    if let Some(selection) = preferred_match {
        return Ok(selection);
    }
    if let Some(needle) = preferred {
        return Err(format!(
            "gpu_renderer_unavailable: requested adapter not found: {needle}"
        ));
    }
    if let Some(selection) = integrated {
        return Ok(selection);
    }
    Err("gpu_renderer_unavailable: no discrete or integrated adapter".into())
}

pub fn diagnostic(info: &AdapterInfo, limits: &wgpu::Limits) -> String {
    format!(
        "adapter={} type={:?} backend={:?} max_texture_dimension_2d={} max_buffer_size={}",
        info.name,
        info.device_type,
        info.backend,
        limits.max_texture_dimension_2d,
        limits.max_buffer_size
    )
}

#[cfg(test)]
mod tests {
    use super::*;
    fn info(kind: DeviceType) -> AdapterInfo {
        AdapterInfo {
            name: "test".into(),
            vendor: 1,
            device: 2,
            device_type: kind,
            driver: "test".into(),
            driver_info: "".into(),
            backend: wgpu::Backend::Vulkan,
        }
    }
    #[test]
    fn software_is_not_hardware() {
        assert!(!matches!(
            info(DeviceType::Cpu).device_type,
            DeviceType::DiscreteGpu | DeviceType::IntegratedGpu
        ));
    }
    #[test]
    fn discrete_preference_order_is_explicit() {
        let kinds = [DeviceType::IntegratedGpu, DeviceType::DiscreteGpu];
        assert_eq!(
            kinds.iter().find(|k| **k == DeviceType::DiscreteGpu),
            Some(&DeviceType::DiscreteGpu)
        );
    }
    #[test]
    fn diagnostic_contains_limits() {
        let d = diagnostic(&info(DeviceType::IntegratedGpu), &wgpu::Limits::default());
        assert!(d.contains("max_texture_dimension_2d"));
    }

    #[test]
    fn integrated_hardware_adapter_is_accepted() {
        let instance = Instance::default();
        if !instance
            .enumerate_adapters(wgpu::Backends::all())
            .iter()
            .any(|adapter| adapter.get_info().device_type == DeviceType::IntegratedGpu)
        {
            return;
        }
        assert!(select(&instance).is_ok());
    }
}
