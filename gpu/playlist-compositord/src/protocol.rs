use crate::contracts::GpuFrame;
use serde::{Deserialize, Serialize};

pub const PROTOCOL_VERSION: u16 = 1;

#[derive(Debug, Serialize, Deserialize, PartialEq, Eq)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum Request {
    Hello {
        version: u16,
        session: String,
    },
    Health,
    Shutdown,
    Render {
        width: u32,
        height: u32,
        sequence: u64,
        pts_ns: i64,
    },
}

#[derive(Debug, Serialize, Deserialize, PartialEq, Eq)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum Response {
    HelloAck {
        version: u16,
        session: String,
        adapter: String,
    },
    Health {
        ready: bool,
        protocol: u16,
    },
    Error {
        code: String,
        message: String,
    },
    Bye,
    Frame {
        frame: GpuFrame,
    },
}

pub fn encode<T: Serialize>(value: &T) -> Result<String, serde_json::Error> {
    serde_json::to_string(value)
}
pub fn decode_request(line: &str) -> Result<Request, serde_json::Error> {
    serde_json::from_str(line)
}
