use memmap2::{MmapMut, MmapOptions};
use std::fs::{File, OpenOptions};
use std::io;
use std::path::{Path, PathBuf};

/// Cross-process bounded frame storage primitive. The file name is the stable
/// session mapping name; on Windows it is placed under the user's local temp
/// directory and on POSIX under the runtime temp directory. A higher-level
/// ring header owns sequence/length validation.
pub struct SharedMapping {
    path: PathBuf,
    file: File,
    map: MmapMut,
}

impl SharedMapping {
    pub fn create(path: impl AsRef<Path>, len: usize) -> io::Result<Self> {
        if len == 0 {
            return Err(io::Error::new(
                io::ErrorKind::InvalidInput,
                "mapping length must be positive",
            ));
        }
        let path = path.as_ref().to_path_buf();
        let file = OpenOptions::new()
            .create(true)
            .read(true)
            .write(true)
            .open(&path)?;
        file.set_len(len as u64)?;
        let map = unsafe { MmapOptions::new().len(len).map_mut(&file)? };
        Ok(Self { path, file, map })
    }
    pub fn open(path: impl AsRef<Path>, len: usize) -> io::Result<Self> {
        let path = path.as_ref().to_path_buf();
        let file = OpenOptions::new().read(true).write(true).open(&path)?;
        if file.metadata()?.len() < len as u64 {
            return Err(io::Error::new(
                io::ErrorKind::UnexpectedEof,
                "mapping is smaller than requested",
            ));
        }
        let map = unsafe { MmapOptions::new().len(len).map_mut(&file)? };
        Ok(Self { path, file, map })
    }
    pub fn bytes(&self) -> &[u8] {
        &self.map
    }
    pub fn bytes_mut(&mut self) -> &mut [u8] {
        &mut self.map
    }
    pub fn flush(&mut self) -> io::Result<()> {
        self.map.flush()
    }
    pub fn path(&self) -> &Path {
        &self.path
    }
}

impl Drop for SharedMapping {
    fn drop(&mut self) {
        let _ = self.map.flush();
        let _ = self.file.sync_data();
    }
}

#[cfg(test)]
mod tests {
    use super::SharedMapping;
    use std::time::{SystemTime, UNIX_EPOCH};
    #[test]
    fn mapping_is_visible_after_reopen() {
        let p = std::env::temp_dir().join(format!(
            "imagepad-gpu-map-{}",
            SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        let mut a = SharedMapping::create(&p, 64).unwrap();
        a.bytes_mut()[0..4].copy_from_slice(b"GPU1");
        a.flush().unwrap();
        let b = SharedMapping::open(&p, 64).unwrap();
        assert_eq!(&b.bytes()[0..4], b"GPU1");
        drop(b);
        drop(a);
        let _ = std::fs::remove_file(p);
    }
}
