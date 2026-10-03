use serde::Deserialize;
use std::io::{self, Read, Write};
use xpost_compositord::{Asset, Frame, Renderer, Update};

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct Init {
    width: u32,
    height: u32,
    assets: Vec<Asset>,
    #[serde(default)]
    light_background: bool,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct Request {
    frame: Frame,
    update: Option<Update>,
}

fn run() -> Result<(), String> {
    let mut input = io::stdin().lock();
    let mut output = io::stdout().lock();
    let init_bytes = xpost_compositord::protocol::read_control(&mut input)?;
    let init: Init =
        serde_json::from_slice(&init_bytes).map_err(|e| format!("decoding init message: {e}"))?;
    let mut renderer = Renderer::new(init.width, init.height, &init.assets)?;
    renderer.set_light_background(init.light_background);
    let ready =
        serde_json::to_vec(renderer.ready()).map_err(|e| format!("encoding ready message: {e}"))?;
    xpost_compositord::protocol::write_control(&mut output, &ready)
        .map_err(|e| format!("writing ready message: {e}"))?;
    output
        .flush()
        .map_err(|e| format!("flushing ready message: {e}"))?;

    while let Some(request_bytes) = xpost_compositord::protocol::read_control_or_eof(&mut input)? {
        let request: Request = serde_json::from_slice(&request_bytes)
            .map_err(|e| format!("decoding frame request: {e}"))?;
        renderer.validate_request(&request.frame, request.update.as_ref())?;
        let updated = match request.update {
            Some(update) => {
                if update.byte_len == 0
                    || update.byte_len > xpost_compositord::protocol::MAX_ASSET_BYTES
                {
                    return Err(format!(
                        "update for {} has invalid byte length",
                        update.asset_id
                    ));
                }
                let mut pixels = vec![0u8; update.byte_len as usize];
                input
                    .read_exact(&mut pixels)
                    .map_err(|e| format!("reading update RGBA pixels: {e}"))?;
                Some((update, pixels))
            }
            None => None,
        };
        let frame_bytes = match updated {
            Some((update, pixels)) => renderer.render(&request.frame, Some((&update, &pixels)))?,
            None => renderer.render(&request.frame, None)?,
        };
        output
            .write_all(&frame_bytes)
            .map_err(|e| format!("writing RGBA frame: {e}"))?;
        output
            .flush()
            .map_err(|e| format!("flushing RGBA frame: {e}"))?;
    }
    Ok(())
}

fn main() {
    let args: Vec<String> = std::env::args().skip(1).collect();
    if args.iter().any(|arg| arg == "--help" || arg == "-h") {
        println!("xpost-compositord: persistent WGPU RGBA compositor; communicates over stdin/stdout using framed JSON control messages and raw RGBA frame bytes");
        return;
    }
    if !args.is_empty() {
        eprintln!("unsupported arguments; use --help");
        std::process::exit(2);
    }
    if let Err(error) = run() {
        let _ = writeln!(io::stderr(), "xpost-compositord: {error}");
        std::process::exit(1);
    }
}
