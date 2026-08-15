use fontdue::{Font, FontSettings};

pub const GLYPH_ATLAS_CELL_SIZE: u32 = 64;
pub const GLYPH_ATLAS_COLUMNS: u32 = 16;
pub const GLYPH_ATLAS_FIRST_CODEPOINT: u32 = 32;
pub const GLYPH_ATLAS_LAST_CODEPOINT: u32 = 126;
pub const GLYPH_ATLAS_ASCII_GLYPH_COUNT: u32 =
    GLYPH_ATLAS_LAST_CODEPOINT - GLYPH_ATLAS_FIRST_CODEPOINT + 1;
/// Extra codepoints needed by the first real SoundCloud metadata fixture.
/// Keep this explicit until the production scene contract carries a general
/// glyph-map buffer and shaping/fallback policy.
pub const GLYPH_ATLAS_EXTRA_CODEPOINTS: [u32; 2] = [0x8D64, 0x6708]; // 赤月
pub const GLYPH_ATLAS_GLYPH_COUNT: u32 =
    GLYPH_ATLAS_ASCII_GLYPH_COUNT + GLYPH_ATLAS_EXTRA_CODEPOINTS.len() as u32;
pub const GLYPH_ATLAS_ROWS: u32 =
    (GLYPH_ATLAS_GLYPH_COUNT + GLYPH_ATLAS_COLUMNS - 1) / GLYPH_ATLAS_COLUMNS;
pub const GLYPH_ATLAS_STYLE_HEIGHT: u32 = GLYPH_ATLAS_ROWS * GLYPH_ATLAS_CELL_SIZE;
pub const GLYPH_ATLAS_STYLE_COUNT: u32 = 3;
pub const GLYPH_ATLAS_METRIC_COUNT: u32 = GLYPH_ATLAS_STYLE_COUNT * GLYPH_ATLAS_GLYPH_COUNT;
pub const GLYPH_ATLAS_WIDTH: u32 = GLYPH_ATLAS_COLUMNS * GLYPH_ATLAS_CELL_SIZE;
pub const GLYPH_ATLAS_HEIGHT: u32 = GLYPH_ATLAS_STYLE_HEIGHT * GLYPH_ATLAS_STYLE_COUNT;
pub const GLYPH_ATLAS_FONT_SIZE: f32 = 48.0;

const FONT_BYTES: [&[u8]; 3] = [
    include_bytes!("../../../internal/video/fonts/NotoSansJP-Regular.ttf"),
    include_bytes!("../../../internal/video/fonts/NotoSansJP-Medium.ttf"),
    include_bytes!("../../../internal/video/fonts/NotoSansJP-SemiBold.ttf"),
];

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum GlyphStyle {
    Regular,
    Medium,
    SemiBold,
}

impl GlyphStyle {
    pub fn atlas_layer(self) -> u32 {
        match self {
            Self::Regular => 0,
            Self::Medium => 1,
            Self::SemiBold => 2,
        }
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct GlyphAtlas {
    pub width: u32,
    pub height: u32,
    /// RGBA8 pixels. Glyph coverage is stored in alpha; RGB is white.
    pub pixels: Vec<u8>,
    /// Per-style, per-codepoint horizontal advances normalized to a 64px cell,
    /// encoded as unsigned Q16 values.
    pub advances_q16: Vec<u32>,
}

impl GlyphAtlas {
    pub fn alpha_coverage_for(&self, character: char) -> usize {
        self.alpha_coverage_for_style(GlyphStyle::Regular, character)
    }

    pub fn alpha_coverage_for_style(&self, style: GlyphStyle, character: char) -> usize {
        let Some(index) = atlas_index(character) else {
            return 0;
        };
        let cell_x = (index % GLYPH_ATLAS_COLUMNS) * GLYPH_ATLAS_CELL_SIZE;
        let cell_y = style.atlas_layer() * GLYPH_ATLAS_STYLE_HEIGHT
            + (index / GLYPH_ATLAS_COLUMNS) * GLYPH_ATLAS_CELL_SIZE;
        let mut coverage = 0;
        for y in cell_y..cell_y + GLYPH_ATLAS_CELL_SIZE {
            for x in cell_x..cell_x + GLYPH_ATLAS_CELL_SIZE {
                let offset = ((y * self.width + x) * 4 + 3) as usize;
                if self.pixels[offset] != 0 {
                    coverage += 1;
                }
            }
        }
        coverage
    }

    pub fn glyph_advance_q16(&self, style: GlyphStyle, character: char) -> u32 {
        let Some(index) = atlas_index(character) else {
            return 0;
        };
        self.advances_q16[(style.atlas_layer() * GLYPH_ATLAS_GLYPH_COUNT + index) as usize]
    }
}

pub fn build_atlas_for_text(lines: [&str; 4]) -> Result<GlyphAtlas, String> {
    for line in lines {
        for character in line.chars() {
            if atlas_index(character).is_none() {
                return Err(format!(
                    "Draft font atlas does not contain codepoint U+{:04X}",
                    character as u32
                ));
            }
        }
    }

    let fonts = FONT_BYTES
        .iter()
        .enumerate()
        .map(|(index, bytes)| {
            Font::from_bytes(*bytes, FontSettings::default())
                .map_err(|error| format!("load bundled Noto Sans JP style {index}: {error:?}"))
        })
        .collect::<Result<Vec<_>, _>>()?;
    let mut pixels = vec![0u8; (GLYPH_ATLAS_WIDTH * GLYPH_ATLAS_HEIGHT * 4) as usize];
    let mut advances_q16 = vec![0u32; GLYPH_ATLAS_METRIC_COUNT as usize];

    let mut codepoints: Vec<u32> =
        (GLYPH_ATLAS_FIRST_CODEPOINT..=GLYPH_ATLAS_LAST_CODEPOINT).collect();
    codepoints.extend(GLYPH_ATLAS_EXTRA_CODEPOINTS);

    for (style_index, font) in fonts.iter().enumerate() {
        for (index, codepoint) in codepoints.iter().copied().enumerate() {
            let character = char::from_u32(codepoint).expect("valid atlas codepoint");
            let (metrics, bitmap) = font.rasterize(character, GLYPH_ATLAS_FONT_SIZE);
            advances_q16[(style_index as u32 * GLYPH_ATLAS_GLYPH_COUNT + index as u32) as usize] =
                ((metrics.advance_width / GLYPH_ATLAS_CELL_SIZE as f32) * 65536.0)
                    .round()
                    .max(1.0) as u32;
            if metrics.width == 0 || metrics.height == 0 {
                continue;
            }

            let index = index as u32;
            let cell_x = (index % GLYPH_ATLAS_COLUMNS) * GLYPH_ATLAS_CELL_SIZE;
            let cell_y = style_index as u32 * GLYPH_ATLAS_STYLE_HEIGHT
                + (index / GLYPH_ATLAS_COLUMNS) * GLYPH_ATLAS_CELL_SIZE;
            let origin_x =
                cell_x as i32 + (GLYPH_ATLAS_CELL_SIZE as i32 - metrics.width as i32) / 2;
            let origin_y =
                cell_y as i32 + (GLYPH_ATLAS_CELL_SIZE as i32 - metrics.height as i32) / 2;

            for source_y in 0..metrics.height {
                for source_x in 0..metrics.width {
                    let destination_x = origin_x + source_x as i32;
                    let destination_y = origin_y + source_y as i32;
                    if destination_x < 0
                        || destination_y < 0
                        || destination_x >= GLYPH_ATLAS_WIDTH as i32
                        || destination_y >= GLYPH_ATLAS_HEIGHT as i32
                    {
                        continue;
                    }
                    let alpha = bitmap[source_y * metrics.width + source_x];
                    let offset = ((destination_y as u32 * GLYPH_ATLAS_WIDTH + destination_x as u32)
                        * 4) as usize;
                    pixels[offset] = 255;
                    pixels[offset + 1] = 255;
                    pixels[offset + 2] = 255;
                    pixels[offset + 3] = alpha;
                }
            }
        }
    }

    Ok(GlyphAtlas {
        width: GLYPH_ATLAS_WIDTH,
        height: GLYPH_ATLAS_HEIGHT,
        pixels,
        advances_q16,
    })
}

pub fn atlas_index(character: char) -> Option<u32> {
    let codepoint = character as u32;
    if (GLYPH_ATLAS_FIRST_CODEPOINT..=GLYPH_ATLAS_LAST_CODEPOINT).contains(&codepoint) {
        return Some(codepoint - GLYPH_ATLAS_FIRST_CODEPOINT);
    }
    GLYPH_ATLAS_EXTRA_CODEPOINTS
        .iter()
        .position(|extra| *extra == codepoint)
        .map(|index| GLYPH_ATLAS_ASCII_GLYPH_COUNT + index as u32)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn rasterizes_fixture_text_into_weighted_real_font_atlas() {
        let atlas = build_atlas_for_text(["IMAGEPAD", "GPU PARITY", "CANONICAL SCENE", "0:00"])
            .expect("bundled font should rasterize");

        assert_eq!(atlas.width, GLYPH_ATLAS_WIDTH);
        assert_eq!(atlas.height, GLYPH_ATLAS_HEIGHT);
        assert_eq!(GLYPH_ATLAS_STYLE_COUNT, 3);
        assert_eq!(
            atlas.pixels.len(),
            (atlas.width * atlas.height * 4) as usize
        );
        assert_eq!(atlas.advances_q16.len(), GLYPH_ATLAS_METRIC_COUNT as usize);
        assert!(atlas.alpha_coverage_for_style(GlyphStyle::Regular, 'I') > 0);
        assert!(atlas.alpha_coverage_for_style(GlyphStyle::Medium, 'M') > 0);
        assert!(atlas.alpha_coverage_for_style(GlyphStyle::SemiBold, 'M') > 0);
        assert!(
            atlas.glyph_advance_q16(GlyphStyle::Regular, 'I')
                < atlas.glyph_advance_q16(GlyphStyle::Regular, 'M')
        );
        assert!(atlas.glyph_advance_q16(GlyphStyle::Regular, ' ') > 0);
        assert_ne!(
            atlas.alpha_coverage_for_style(GlyphStyle::Regular, 'M'),
            atlas.alpha_coverage_for_style(GlyphStyle::SemiBold, 'M')
        );
        assert_eq!(atlas.alpha_coverage_for_style(GlyphStyle::Regular, ' '), 0);
    }

    #[test]
    fn rasterizes_soundcloud_japanese_artist_glyphs() {
        let atlas = build_atlas_for_text(["odd", "赤月", "", "0:00"])
            .expect("SoundCloud artist glyphs should rasterize");
        assert!(atlas.alpha_coverage_for_style(GlyphStyle::Medium, '赤') > 0);
        assert!(atlas.alpha_coverage_for_style(GlyphStyle::Medium, '月') > 0);
        assert!(atlas.glyph_advance_q16(GlyphStyle::Medium, '赤') > 0);
        assert!(atlas.glyph_advance_q16(GlyphStyle::Medium, '月') > 0);
    }
}
