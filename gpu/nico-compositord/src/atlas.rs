use std::cmp::Reverse;
use std::collections::{BTreeMap, HashSet};
use std::fmt;

use crate::protocol::{MAX_ASSETS, MAX_ASSET_BYTES, MAX_ASSET_DIMENSION, MAX_TOTAL_ASSET_BYTES};

const CANDIDATE_PAGE_WIDTHS: [u32; 5] = [256, 512, 1024, 2048, 4096];
const MAX_REGULAR_PAGE_HEIGHT: u32 = 4096;
const MAX_ATLAS_BYTES: u64 = 256 * 1024 * 1024;
const ATLAS_BUDGET_HEADROOM: u64 = 1024 * 1024;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct AssetExtent {
    pub id: u32,
    pub width: u32,
    pub height: u32,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct AtlasRegion {
    pub page: u32,
    pub x: u32,
    pub y: u32,
    pub width: u32,
    pub height: u32,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct AtlasPage {
    pub width: u32,
    pub height: u32,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct AtlasPlan {
    pub pages: Vec<AtlasPage>,
    pub regions: BTreeMap<u32, AtlasRegion>,
    pub source_bytes: u64,
    pub allocated_bytes: u64,
    pub byte_budget: u64,
    pub candidate_width: u32,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum AtlasDecision {
    Packed(AtlasPlan),
    Separate(SeparateReason),
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum SeparateReason {
    NoCandidatePageWidth,
    BudgetExceeded {
        allocated_bytes: u64,
        budget_bytes: u64,
    },
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum AtlasError {
    TooManyAssets,
    DuplicateAssetId(u32),
    InvalidDimensions(u32),
    AssetDimensionExceeded(u32),
    AssetExceedsDeviceLimit {
        id: u32,
        width: u32,
        height: u32,
        limit: u32,
    },
    AssetByteLengthOverflow(u32),
    AssetByteLimitExceeded(u32),
    TotalAssetBytesExceeded,
    AllocationSizeOverflow,
}

impl fmt::Display for AtlasError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::TooManyAssets => f.write_str("atlas asset count exceeds the NCT1 limit"),
            Self::DuplicateAssetId(id) => write!(f, "duplicate atlas asset id {id}"),
            Self::InvalidDimensions(id) => write!(f, "atlas asset {id} has zero dimensions"),
            Self::AssetDimensionExceeded(id) => {
                write!(f, "atlas asset {id} exceeds the NCT1 dimension limit")
            }
            Self::AssetExceedsDeviceLimit {
                id,
                width,
                height,
                limit,
            } => write!(
                f,
                "atlas asset {id} ({width}x{height}) exceeds device texture limit {limit}"
            ),
            Self::AssetByteLengthOverflow(id) => {
                write!(f, "atlas asset {id} RGBA byte length overflows")
            }
            Self::AssetByteLimitExceeded(id) => {
                write!(f, "atlas asset {id} exceeds the NCT1 byte limit")
            }
            Self::TotalAssetBytesExceeded => {
                f.write_str("atlas source assets exceed the NCT1 total byte limit")
            }
            Self::AllocationSizeOverflow => f.write_str("atlas allocation size overflows"),
        }
    }
}

impl std::error::Error for AtlasError {}

/// Plan deterministic, non-rotating RGBA atlas pages without allocating pixel buffers.
/// A valid scene that should retain its individual textures returns `Separate`.
pub fn plan_atlas(
    assets: &[AssetExtent],
    max_texture_dimension: u32,
) -> Result<AtlasDecision, AtlasError> {
    let (sorted, source_bytes) = validate_and_sort_assets(assets, max_texture_dimension)?;
    let byte_budget = source_bytes
        .checked_mul(2)
        .and_then(|bytes| bytes.checked_add(ATLAS_BUDGET_HEADROOM))
        .unwrap_or(MAX_ATLAS_BYTES)
        .min(MAX_ATLAS_BYTES);
    plan_validated_assets(&sorted, source_bytes, max_texture_dimension, byte_budget)
}

#[cfg(test)]
fn plan_atlas_with_budget(
    assets: &[AssetExtent],
    max_texture_dimension: u32,
    byte_budget: u64,
) -> Result<AtlasDecision, AtlasError> {
    let (sorted, source_bytes) = validate_and_sort_assets(assets, max_texture_dimension)?;
    plan_validated_assets(&sorted, source_bytes, max_texture_dimension, byte_budget)
}

fn validate_and_sort_assets(
    assets: &[AssetExtent],
    max_texture_dimension: u32,
) -> Result<(Vec<AssetExtent>, u64), AtlasError> {
    if assets.len() > MAX_ASSETS as usize {
        return Err(AtlasError::TooManyAssets);
    }

    let mut ids = HashSet::with_capacity(assets.len());
    let mut source_bytes = 0u64;
    for asset in assets {
        if !ids.insert(asset.id) {
            return Err(AtlasError::DuplicateAssetId(asset.id));
        }
        if asset.width == 0 || asset.height == 0 {
            return Err(AtlasError::InvalidDimensions(asset.id));
        }
        let byte_length = (asset.width as u64)
            .checked_mul(asset.height as u64)
            .and_then(|pixels| pixels.checked_mul(4))
            .ok_or(AtlasError::AssetByteLengthOverflow(asset.id))?;
        if asset.width > MAX_ASSET_DIMENSION || asset.height > MAX_ASSET_DIMENSION {
            return Err(AtlasError::AssetDimensionExceeded(asset.id));
        }
        if asset.width > max_texture_dimension || asset.height > max_texture_dimension {
            return Err(AtlasError::AssetExceedsDeviceLimit {
                id: asset.id,
                width: asset.width,
                height: asset.height,
                limit: max_texture_dimension,
            });
        }
        if byte_length > MAX_ASSET_BYTES {
            return Err(AtlasError::AssetByteLimitExceeded(asset.id));
        }
        source_bytes = source_bytes
            .checked_add(byte_length)
            .ok_or(AtlasError::TotalAssetBytesExceeded)?;
        if source_bytes > MAX_TOTAL_ASSET_BYTES {
            return Err(AtlasError::TotalAssetBytesExceeded);
        }
    }

    let mut sorted = assets.to_vec();
    sorted.sort_by_key(|asset| (Reverse(asset.height), Reverse(asset.width), asset.id));
    Ok((sorted, source_bytes))
}

fn plan_validated_assets(
    sorted_assets: &[AssetExtent],
    source_bytes: u64,
    max_texture_dimension: u32,
    byte_budget: u64,
) -> Result<AtlasDecision, AtlasError> {
    if sorted_assets.is_empty() {
        return Ok(AtlasDecision::Packed(AtlasPlan {
            pages: Vec::new(),
            regions: BTreeMap::new(),
            source_bytes,
            allocated_bytes: 0,
            byte_budget,
            candidate_width: 0,
        }));
    }

    let regular_page_height = MAX_REGULAR_PAGE_HEIGHT.min(max_texture_dimension);
    let candidates: Vec<_> = CANDIDATE_PAGE_WIDTHS
        .into_iter()
        .filter(|width| *width <= max_texture_dimension)
        .collect();
    if candidates.is_empty() {
        return Ok(AtlasDecision::Separate(
            SeparateReason::NoCandidatePageWidth,
        ));
    }

    let mut best: Option<AtlasPlan> = None;
    for candidate_width in candidates {
        let plan = pack_for_candidate(
            sorted_assets,
            source_bytes,
            candidate_width,
            regular_page_height,
            byte_budget,
        )?;
        let is_better = best.as_ref().is_none_or(|current| {
            (plan.allocated_bytes, plan.pages.len(), plan.candidate_width)
                < (
                    current.allocated_bytes,
                    current.pages.len(),
                    current.candidate_width,
                )
        });
        if is_better {
            best = Some(plan);
        }
    }

    let plan = best.expect("nonempty candidate list has a packed plan");
    if plan.allocated_bytes > byte_budget {
        return Ok(AtlasDecision::Separate(SeparateReason::BudgetExceeded {
            allocated_bytes: plan.allocated_bytes,
            budget_bytes: byte_budget,
        }));
    }
    Ok(AtlasDecision::Packed(plan))
}

#[derive(Clone, Copy, Debug)]
struct WorkingPage {
    width_limit: u32,
    height_limit: u32,
    cursor_x: u32,
    cursor_y: u32,
    row_height: u32,
    used_width: u32,
    used_height: u32,
}

impl WorkingPage {
    fn regular(width_limit: u32, height_limit: u32) -> Self {
        Self {
            width_limit,
            height_limit,
            cursor_x: 0,
            cursor_y: 0,
            row_height: 0,
            used_width: 0,
            used_height: 0,
        }
    }

    fn single(width: u32, height: u32) -> Self {
        Self {
            width_limit: width,
            height_limit: height,
            cursor_x: width,
            cursor_y: 0,
            row_height: height,
            used_width: width,
            used_height: height,
        }
    }

    fn place(&mut self, asset: AssetExtent) -> Option<(u32, u32)> {
        let mut x = self.cursor_x;
        let mut y = self.cursor_y;
        let mut row_height = self.row_height;

        if x.checked_add(asset.width)? > self.width_limit {
            y = y.checked_add(row_height)?;
            x = 0;
            row_height = 0;
        }
        if y.checked_add(asset.height)? > self.height_limit {
            return None;
        }

        self.cursor_x = x.checked_add(asset.width)?;
        self.cursor_y = y;
        self.row_height = row_height.max(asset.height);
        self.used_width = self.used_width.max(self.cursor_x);
        self.used_height = self.used_height.max(y.checked_add(asset.height)?);
        Some((x, y))
    }

    fn finish(self) -> AtlasPage {
        AtlasPage {
            width: self.used_width,
            height: self.used_height,
        }
    }
}

fn pack_for_candidate(
    sorted_assets: &[AssetExtent],
    source_bytes: u64,
    candidate_width: u32,
    regular_page_height: u32,
    byte_budget: u64,
) -> Result<AtlasPlan, AtlasError> {
    let mut pages = Vec::<WorkingPage>::new();
    let mut current_regular_page = None;
    let mut regions = BTreeMap::new();

    for &asset in sorted_assets {
        if asset.width > candidate_width || asset.height > regular_page_height {
            let page =
                u32::try_from(pages.len()).map_err(|_| AtlasError::AllocationSizeOverflow)?;
            pages.push(WorkingPage::single(asset.width, asset.height));
            regions.insert(
                asset.id,
                AtlasRegion {
                    page,
                    x: 0,
                    y: 0,
                    width: asset.width,
                    height: asset.height,
                },
            );
            current_regular_page = None;
            continue;
        }

        let mut placed = current_regular_page.and_then(|page_index: usize| {
            pages[page_index]
                .place(asset)
                .map(|(x, y)| (page_index, x, y))
        });
        if placed.is_none() {
            let page_index = pages.len();
            let mut page = WorkingPage::regular(candidate_width, regular_page_height);
            let (x, y) = page
                .place(asset)
                .expect("a regular page fits every classified asset");
            pages.push(page);
            current_regular_page = Some(page_index);
            placed = Some((page_index, x, y));
        }
        let (page_index, x, y) = placed.expect("regular asset was placed");
        let page = u32::try_from(page_index).map_err(|_| AtlasError::AllocationSizeOverflow)?;
        regions.insert(
            asset.id,
            AtlasRegion {
                page,
                x,
                y,
                width: asset.width,
                height: asset.height,
            },
        );
    }

    let finished_pages: Vec<_> = pages.into_iter().map(WorkingPage::finish).collect();
    let mut allocated_bytes = 0u64;
    for page in &finished_pages {
        let page_bytes = (page.width as u64)
            .checked_mul(page.height as u64)
            .and_then(|pixels| pixels.checked_mul(4))
            .ok_or(AtlasError::AllocationSizeOverflow)?;
        allocated_bytes = allocated_bytes
            .checked_add(page_bytes)
            .ok_or(AtlasError::AllocationSizeOverflow)?;
    }

    Ok(AtlasPlan {
        pages: finished_pages,
        regions,
        source_bytes,
        allocated_bytes,
        byte_budget,
        candidate_width,
    })
}

#[cfg(test)]
mod tests {
    use super::{plan_atlas, plan_atlas_with_budget, AssetExtent, AtlasDecision, AtlasError};

    fn extent(id: u32, width: u32, height: u32) -> AssetExtent {
        AssetExtent { id, width, height }
    }

    fn packed(decision: AtlasDecision) -> super::AtlasPlan {
        match decision {
            AtlasDecision::Packed(plan) => plan,
            AtlasDecision::Separate(reason) => panic!("unexpected separate fallback: {reason:?}"),
        }
    }

    fn assert_regions_are_valid(plan: &super::AtlasPlan) {
        for (&asset_id, region) in &plan.regions {
            assert!(region.width > 0 && region.height > 0, "asset {asset_id}");
            let page = &plan.pages[region.page as usize];
            assert!(region.x + region.width <= page.width, "asset {asset_id}");
            assert!(region.y + region.height <= page.height, "asset {asset_id}");
        }

        let entries: Vec<_> = plan.regions.iter().collect();
        for left in 0..entries.len() {
            let (&left_id, a) = entries[left];
            for (right_id, b) in entries.iter().skip(left + 1) {
                if a.page != b.page {
                    continue;
                }
                let separated = a.x + a.width <= b.x
                    || b.x + b.width <= a.x
                    || a.y + a.height <= b.y
                    || b.y + b.height <= a.y;
                assert!(separated, "assets {left_id} and {right_id} overlap");
            }
        }
    }

    #[test]
    fn empty_scene_packs_without_allocating_pages() {
        let plan = packed(plan_atlas(&[], 8192).unwrap());
        assert!(plan.pages.is_empty());
        assert!(plan.regions.is_empty());
        assert_eq!(plan.allocated_bytes, 0);
    }

    #[test]
    fn candidate_width_selection_is_deterministic_and_uses_occupied_bounds() {
        let assets: Vec<_> = (0..33).map(|id| extent(id, 8, 8)).collect();
        let mut reversed = assets.clone();
        reversed.reverse();

        let first = packed(plan_atlas(&assets, 8192).unwrap());
        let second = packed(plan_atlas(&reversed, 8192).unwrap());
        assert_eq!(first, second);
        assert_eq!(first.pages.len(), 1);
        assert_eq!((first.pages[0].width, first.pages[0].height), (264, 8));
        assert_eq!(first.allocated_bytes, 264 * 8 * 4);
        assert_eq!(first.regions[&0].x, 0);
        assert_eq!(first.regions[&31].x, 248);
        assert_eq!(first.regions[&32].x, 256);
        assert_regions_are_valid(&first);
    }

    #[test]
    fn odd_and_skinny_assets_keep_exact_integer_extents() {
        let plan = packed(
            plan_atlas(
                &[
                    extent(9, 1, 17),
                    extent(3, 15, 1),
                    extent(7, 7, 11),
                    extent(2, 5, 3),
                ],
                8192,
            )
            .unwrap(),
        );
        assert_eq!((plan.regions[&9].width, plan.regions[&9].height), (1, 17));
        assert_eq!((plan.regions[&3].width, plan.regions[&3].height), (15, 1));
        assert_eq!((plan.regions[&7].width, plan.regions[&7].height), (7, 11));
        assert_eq!((plan.regions[&2].width, plan.regions[&2].height), (5, 3));
        assert_regions_are_valid(&plan);
    }

    #[test]
    fn large_metadata_scene_uses_multiple_pages_without_pixel_fixtures() {
        let assets: Vec<_> = (0..128).map(|id| extent(id, 128, 2048)).collect();
        let plan = packed(plan_atlas(&assets, 8192).unwrap());
        assert_eq!(plan.regions.len(), 128);
        assert_eq!(plan.pages.len(), 2);
        assert_eq!(plan.allocated_bytes, 128 * 128 * 2048 * 4);
        assert_regions_are_valid(&plan);
    }

    #[test]
    fn asset_larger_than_normal_page_uses_an_exact_single_page() {
        let plan = packed(plan_atlas(&[extent(1, 5000, 3), extent(2, 7, 9)], 8192).unwrap());
        let region = plan.regions[&1];
        let page = &plan.pages[region.page as usize];
        assert_eq!((page.width, page.height), (5000, 3));
        assert_eq!(
            (region.x, region.y, region.width, region.height),
            (0, 0, 5000, 3)
        );
        assert_regions_are_valid(&plan);
    }

    #[test]
    fn byte_budget_boundary_falls_back_only_when_the_plan_exceeds_it() {
        let assets = [extent(4, 8, 8)];
        let at_limit = plan_atlas_with_budget(&assets, 8192, 256).unwrap();
        let below_limit = plan_atlas_with_budget(&assets, 8192, 255).unwrap();
        assert!(matches!(at_limit, AtlasDecision::Packed(_)));
        assert!(matches!(below_limit, AtlasDecision::Separate(_)));
    }

    #[test]
    fn valid_scene_falls_back_when_no_standard_page_width_fits_device() {
        let decision = plan_atlas(&[extent(1, 8, 8)], 128).unwrap();
        assert!(matches!(decision, AtlasDecision::Separate(_)));
    }

    #[test]
    fn duplicate_ids_zero_dimensions_overflow_and_device_limits_are_rejected() {
        assert!(matches!(
            plan_atlas(&[extent(1, 8, 8), extent(1, 4, 4)], 8192),
            Err(AtlasError::DuplicateAssetId(1))
        ));
        assert!(plan_atlas(&[extent(1, 0, 8)], 8192).is_err());
        assert!(plan_atlas(&[extent(1, u32::MAX, u32::MAX)], u32::MAX).is_err());
        assert!(plan_atlas(&[extent(1, 129, 8)], 128).is_err());
    }

    #[test]
    fn rejects_total_source_bytes_above_nct_limit() {
        let assets = [
            extent(1, 16_384, 2_048),
            extent(2, 16_384, 2_048),
            extent(3, 16_384, 2_048),
        ];
        assert!(matches!(
            plan_atlas(&assets, 16_384),
            Err(AtlasError::TotalAssetBytesExceeded)
        ));
    }

    #[test]
    fn enforces_per_asset_dimension_byte_and_count_boundaries() {
        let at_dimension_limit = packed(plan_atlas(&[extent(1, 16_384, 1)], 16_384).unwrap());
        assert_eq!(at_dimension_limit.regions[&1].width, 16_384);
        assert!(matches!(
            plan_atlas(&[extent(2, 16_385, 1)], 16_385),
            Err(AtlasError::AssetDimensionExceeded(2))
        ));

        let at_byte_limit = packed(plan_atlas(&[extent(3, 16_384, 2_048)], 16_384).unwrap());
        assert_eq!(at_byte_limit.source_bytes, 128 * 1024 * 1024);
        assert!(matches!(
            plan_atlas(&[extent(4, 16_384, 2_049)], 16_384),
            Err(AtlasError::AssetByteLimitExceeded(4))
        ));

        let too_many: Vec<_> = (0..=10_000).map(|id| extent(id, 1, 1)).collect();
        assert!(matches!(
            plan_atlas(&too_many, 8192),
            Err(AtlasError::TooManyAssets)
        ));
    }

    #[test]
    fn ten_thousand_asset_metadata_stays_bounded_and_deterministic() {
        let assets: Vec<_> = (0..10_000).map(|id| extent(id, 1, 1)).collect();
        let first = packed(plan_atlas(&assets, 8192).unwrap());
        let second = packed(plan_atlas(&assets, 8192).unwrap());
        assert_eq!(first, second);
        assert_eq!(first.regions.len(), 10_000);
        assert!(first.pages.len() <= 10_000);
    }
}
