use crate::protocol::Layer;

#[repr(C)]
#[derive(Clone, Copy, Debug, PartialEq, bytemuck::Pod, bytemuck::Zeroable)]
pub struct Vertex {
    pub position: [f32; 2],
    pub uv: [f32; 2],
    pub opacity: f32,
}

/// Build a top-left-coordinate card quad in NDC, applying Y-axis tilt and a
/// modest perspective projection. The camera is three canvas-width units away.
pub fn vertices(
    layer: &Layer,
    canvas_width: u32,
    canvas_height: u32,
) -> Result<[Vertex; 6], String> {
    if canvas_width == 0 || canvas_height == 0 {
        return Err("canvas dimensions must be non-zero".into());
    }
    let inputs = [
        layer.x,
        layer.y,
        layer.width,
        layer.height,
        layer.tilt,
        layer.opacity,
    ];
    if !inputs.iter().all(|value| value.is_finite())
        || layer.width <= 0.0
        || layer.height <= 0.0
        || !(-89.0..=89.0).contains(&layer.tilt)
        || !(0.0..=1.0).contains(&layer.opacity)
    {
        return Err("layer geometry is outside supported bounds".into());
    }
    let [u0, v0, u1, v1] = layer.uv.unwrap_or([0.0, 0.0, 1.0, 1.0]);
    if ![u0, v0, u1, v1].iter().all(|value| value.is_finite())
        || !(0.0..=1.0).contains(&u0)
        || !(0.0..=1.0).contains(&v0)
        || !(0.0..=1.0).contains(&u1)
        || !(0.0..=1.0).contains(&v1)
        || u0 >= u1
        || v0 >= v1
    {
        return Err("layer UV range is outside supported bounds".into());
    }
    let x1 = layer.x + layer.width;
    let y1 = layer.y + layer.height;
    if !x1.is_finite() || !y1.is_finite() {
        return Err("layer bounds overflow pixel coordinates".into());
    }
    let corners = [
        (layer.x, layer.y, u0, v0),
        (layer.x, y1, u0, v1),
        (x1, layer.y, u1, v0),
        (x1, layer.y, u1, v0),
        (layer.x, y1, u0, v1),
        (x1, y1, u1, v1),
    ];
    let radians = layer.tilt.to_radians();
    let sin = radians.sin();
    let cos = radians.cos();
    let center_x = layer.x + layer.width * 0.5;
    let center_y = layer.y + layer.height * 0.5;
    let camera = 3.0;
    let mut result = [Vertex {
        position: [0.0; 2],
        uv: [0.0; 2],
        opacity: layer.opacity,
    }; 6];
    for (i, (x, y, u, v)) in corners.into_iter().enumerate() {
        let local_x = (x - center_x) * 2.0 / canvas_width as f32;
        let local_y = (y - center_y) * 2.0 / canvas_height as f32;
        let depth = -sin * local_x;
        let camera_depth = camera + depth;
        if !camera_depth.is_finite() || camera_depth <= 0.25 {
            return Err("layer geometry crosses the perspective camera plane".into());
        }
        let perspective = camera / camera_depth;
        let rotated_x = center_x * 2.0 / canvas_width as f32 - 1.0 + cos * local_x * perspective;
        let rotated_y = 1.0 - center_y * 2.0 / canvas_height as f32 - local_y * perspective;
        if !rotated_x.is_finite() || !rotated_y.is_finite() {
            return Err("projected layer geometry is not finite".into());
        }
        result[i] = Vertex {
            position: [rotated_x, rotated_y],
            uv: [u, v],
            opacity: layer.opacity,
        };
    }
    Ok(result)
}

#[cfg(test)]
mod tests {
    use super::vertices;
    use crate::protocol::Layer;

    #[test]
    fn cropped_uv_changes_texture_coordinates_without_changing_quad_positions() {
        let legacy: Layer = serde_json::from_str(
            r#"{"assetId":"tile","x":10.0,"y":20.0,"width":30.0,"height":40.0,"tilt":0.0,"opacity":0.7}"#,
        )
        .unwrap();
        let cropped: Layer = serde_json::from_str(
            r#"{"assetId":"tile","x":10.0,"y":20.0,"width":30.0,"height":40.0,"tilt":0.0,"opacity":0.7,"uv":[0.2,0.1,0.8,0.9]}"#,
        )
        .unwrap();
        let before = vertices(&legacy, 100, 100).unwrap();
        let after = vertices(&cropped, 100, 100).unwrap();
        assert_eq!(
            after.map(|vertex| vertex.position),
            before.map(|vertex| vertex.position),
            "crop must not move or resize the layer"
        );
        assert_eq!(
            after.map(|vertex| vertex.uv),
            [
                [0.2, 0.1],
                [0.2, 0.9],
                [0.8, 0.1],
                [0.8, 0.1],
                [0.2, 0.9],
                [0.8, 0.9],
            ]
        );
        assert_eq!(
            before.map(|vertex| vertex.uv),
            [
                [0.0, 0.0],
                [0.0, 1.0],
                [1.0, 0.0],
                [1.0, 0.0],
                [0.0, 1.0],
                [1.0, 1.0],
            ],
            "legacy records must sample the full image"
        );
    }
}
