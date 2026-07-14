//! OS transport naming. The actual frame payload remains behind a bounded ring;
//! these names are stable so a native shared-memory adapter can be added without
//! changing the wire protocol.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct MappingName(String);
impl MappingName {
    pub fn for_session(session: &str) -> Self {
        let safe: String = session
            .chars()
            .filter(|c| c.is_ascii_alphanumeric() || *c == '-' || *c == '_')
            .collect();
        #[cfg(windows)]
        {
            return Self(format!("Local\\ImagePadServer.PlaylistCompositor.{}", safe));
        }
        #[cfg(not(windows))]
        {
            Self(format!("/tmp/imagepadserver-playlist-compositor-{}", safe))
        }
    }
    pub fn as_str(&self) -> &str {
        &self.0
    }
}

#[cfg(test)]
mod tests {
    use super::MappingName;
    #[test]
    fn names_are_sanitized() {
        let n = MappingName::for_session("a b/secret");
        assert!(!n.as_str().contains(' '));
        assert!(!n.as_str().contains('/'));
    }
}
