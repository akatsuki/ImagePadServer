mod adapter;
mod contracts;
mod gpu_render;
mod hardware_surface;
mod lifecycle;
mod protocol;
mod shared_mapping;
mod shared_ring;
mod transport;

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
                let name = selected.adapter_name().to_string();
                *renderer = Some(selected);
                (
                    Response::HelloAck {
                        version: PROTOCOL_VERSION,
                        session,
                        adapter: name,
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
                match renderer.render_with_scene(width, height, sequence, pts_ns, scene.as_ref()) {
                    Ok(frame) => (Response::Frame { frame }, false),
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
