mod adapter;
mod contracts;
mod gpu_render;
mod hardware_surface;
mod lifecycle;
mod music_v2_contract;
mod protocol;
mod shared_mapping;
mod shared_ring;
mod transport;
mod yuv420;

use protocol::{decode_request, encode, Request, Response, PROTOCOL_VERSION};
use std::io::{self, BufRead, Write};

fn response_for(request: Request, renderer: &mut Option<gpu_render::Renderer>) -> (Response, bool) {
    match request {
        Request::Hello { version, session } if version != PROTOCOL_VERSION => (
            Response::Error {
                code: "protocol_version_mismatch".into(),
                message: format!("supported version {}", PROTOCOL_VERSION),
            },
            false,
        ),
        Request::Hello { session, .. } => match gpu_render::Renderer::new() {
            Err(error) => (
                Response::Error {
                    code: "gpu_renderer_unavailable".into(),
                    // Preserve the adapter/backend diagnostic so the owner can
                    // distinguish missing hardware from backend initialization
                    // or requested-adapter failures in the acceptance report.
                    message: error,
                },
                false,
            ),
            Ok(selected) => {
                let fingerprint = selected.fingerprint();
                *renderer = Some(selected);
                (
                    Response::HelloAck {
                        version: PROTOCOL_VERSION,
                        session,
                        adapter: fingerprint.adapter,
                        backend: fingerprint.backend,
                        toolchain: fingerprint.toolchain,
                        // Both formats are produced by the GPU renderer. The
                        // client still selects RGBA by default for backwards
                        // compatibility.
                        outputs: Some(crate::contracts::OutputCapabilities {
                            schema: crate::contracts::CONTRACT_VERSION,
                            formats: vec![
                                crate::contracts::OutputFormat::Rgba8,
                                crate::contracts::OutputFormat::Yuv420p,
                            ],
                            max_width: crate::contracts::MAX_DIMENSION,
                            max_height: crate::contracts::MAX_DIMENSION,
                            row_alignment: crate::contracts::ROW_ALIGNMENT as u32,
                        }),
                    },
                    false,
                )
            }
        },
        Request::Health => (
            Response::Health {
                ready: renderer.is_some(),
                protocol: PROTOCOL_VERSION,
            },
            false,
        ),
        Request::Render {
            width,
            height,
            sequence,
            pts_ns,
            scene,
            output,
        } => match renderer.as_ref() {
            Some(renderer) => {
                if let Some(scene) = scene.as_ref() {
                    if let Err(error) = scene.validate() {
                        return (
                            Response::Error {
                                code: "invalid_scene".into(),
                                message: format!("{error:?}"),
                            },
                            false,
                        );
                    }
                }
                let rendered = match output {
                    crate::contracts::OutputFormat::Rgba8 => renderer
                        .render_with_scene(width, height, sequence, pts_ns, scene.as_ref())
                        .map(|frame| Response::Frame { frame }),
                    crate::contracts::OutputFormat::Yuv420p => renderer
                        .render_yuv420_with_scene(width, height, sequence, pts_ns, scene.as_ref())
                        .map(|frame| Response::YuvFrame { frame }),
                };
                match rendered {
                    Ok(response) => (response, false),
                    Err(message) => (
                        Response::Error {
                            code: "gpu_render_failed".into(),
                            message,
                        },
                        false,
                    ),
                }
            }
            None => (
                Response::Error {
                    code: "gpu_renderer_unavailable".into(),
                    message: "GPU renderer is not initialized; send Hello first".into(),
                },
                false,
            ),
        },
        Request::RenderV2 { job, .. } => match music_v2_contract::validate_production(&job) {
            Err(message) => (
                Response::Error {
                    code: "invalid_music_render_v2_job".into(),
                    message,
                },
                false,
            ),
            Ok(()) => (
                Response::Error {
                    code: "music_render_v2_not_implemented".into(),
                    message: "V2 contract accepted; native pass graph is not wired yet".into(),
                },
                false,
            ),
        },
        Request::Shutdown => (Response::Bye, true),
    }
}

fn main() -> io::Result<()> {
    if std::env::args()
        .skip(1)
        .any(|arg| arg == "--version" || arg == "-V")
    {
        println!("playlist-compositord {}", env!("CARGO_PKG_VERSION"));
        return Ok(());
    }
    let stdin = io::stdin();
    let mut stdout = io::BufWriter::new(io::stdout());
    let mut renderer = None;
    for line in stdin.lock().lines() {
        let line = line?;
        if line.trim().is_empty() {
            continue;
        }
        let response = match decode_request(&line) {
            Ok(req) => response_for(req, &mut renderer).0,
            Err(e) => Response::Error {
                code: "invalid_request".into(),
                message: e.to_string(),
            },
        };
        writeln!(
            stdout,
            "{}",
            encode(&response).expect("response serializes")
        )?;
        stdout.flush()?;
        if matches!(response, Response::Bye) {
            break;
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn mismatch_is_rejected() {
        let (r, _) = response_for(
            Request::Hello {
                version: 99,
                session: "x".into(),
            },
            &mut None,
        );
        assert!(matches!(r, Response::Error { code, .. } if code == "protocol_version_mismatch"));
    }
}
