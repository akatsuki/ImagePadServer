pub mod protocol;
mod renderer;
mod scene;

pub use protocol::{validate_assets, validate_frame, Asset, Frame, Layer, Update};
pub use renderer::Renderer;
pub use scene::{vertices, Vertex};

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Cursor;

    #[test]
    fn rejects_oversized_canvas_before_any_allocation() {
        let err = validate_assets(16_385, 1, &[]).unwrap_err();
        assert!(err.to_string().contains("canvas"));
    }

    #[test]
    fn rejects_duplicate_asset_ids_and_oversized_pixel_data() {
        let duplicate = vec![
            Asset {
                id: "a".into(),
                path: "a.rgba".into(),
                width: 1,
                height: 1,
            },
            Asset {
                id: "a".into(),
                path: "b.rgba".into(),
                width: 1,
                height: 1,
            },
        ];
        assert!(validate_assets(16, 16, &duplicate).is_err());
        let oversized = vec![Asset {
            id: "huge".into(),
            path: "huge.rgba".into(),
            width: 16_384,
            height: 16_384,
        }];
        assert!(validate_assets(16, 16, &oversized).is_err());
    }

    #[test]
    fn identity_layer_vertices_follow_top_left_pixel_coordinates() {
        let layer = Layer {
            asset_id: "a".into(),
            x: 10.0,
            y: 20.0,
            width: 30.0,
            height: 40.0,
            tilt: 0.0,
            opacity: 1.0,
            uv: None,
        };
        let vertices = vertices(&layer, 100, 100).unwrap();
        assert!((vertices[0].position[0] + 0.8).abs() < 1e-6);
        assert!((vertices[0].position[1] - 0.6).abs() < 1e-6);
        assert!((vertices[1].position[1] + 0.2).abs() < 1e-6);
        assert!((vertices[2].position[0] + 0.2).abs() < 1e-6);
        assert!((vertices[2].position[1] - 0.6).abs() < 1e-6);
        assert_eq!(vertices[0].uv, [0.0, 0.0]);
        assert_eq!(vertices[5].uv, [1.0, 1.0]);
    }

    #[test]
    fn tilted_layer_applies_y_axis_perspective_and_preserves_top_left_input() {
        let layer = Layer {
            asset_id: "a".into(),
            x: 25.0,
            y: 10.0,
            width: 50.0,
            height: 80.0,
            tilt: 45.0,
            opacity: 0.5,
            uv: None,
        };
        let vertices = vertices(&layer, 100, 100).unwrap();
        assert_ne!(vertices[0].position[0], vertices[2].position[0]);
        assert_ne!(vertices[0].position[1], vertices[2].position[1]);
        assert_eq!(vertices[0].opacity, 0.5);
    }

    #[test]
    fn rejects_tilted_geometry_that_crosses_the_perspective_camera() {
        let layer = Layer {
            asset_id: "a".into(),
            x: 0.0,
            y: 0.0,
            width: 1_000.0,
            height: 1.0,
            tilt: 89.0,
            opacity: 1.0,
            uv: None,
        };
        assert!(vertices(&layer, 1, 1).is_err());
    }

    #[test]
    fn protocol_rejects_oversized_control_message_before_reading_body() {
        let prefix = (protocol::MAX_CONTROL_BYTES as u32 + 1).to_le_bytes();
        let mut reader = Cursor::new(prefix);
        let err = protocol::read_control(&mut reader).unwrap_err();
        assert!(err.contains("control message"));
        assert_eq!(
            reader.position(),
            4,
            "oversized length is rejected before body allocation/read"
        );
    }
}
