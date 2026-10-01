use std::collections::{BTreeSet, VecDeque};

use crate::protocol::Draw;

const VPOSS_PER_SECOND: i32 = 100;
const MAX_CACHED_SECONDS: usize = 2;

#[derive(Clone, Copy, Debug)]
struct Event {
    second: i32,
    rank: usize,
    entering: bool,
}

/// Indexes draw intervals into one-second vpos buckets while preserving scene order.
/// The returned indices refer to the caller's draw array and are ordered by
/// owner, comment, primitive, then original position for stable ties.
pub struct SceneIndex {
    render_order: Vec<usize>,
    events: Vec<Event>,
    event_cursor: usize,
    current_second: Option<i32>,
    active: BTreeSet<usize>,
    cached: VecDeque<(i32, Vec<usize>)>,
}

impl SceneIndex {
    pub fn new(draws: &[Draw]) -> Self {
        let mut render_order: Vec<usize> = (0..draws.len()).collect();
        render_order.sort_by_key(|&index| {
            let draw = &draws[index];
            (
                draw.owner_order,
                draw.comment_index,
                draw.primitive_index,
                index,
            )
        });

        let mut events = Vec::with_capacity(render_order.len() * 2);
        for (rank, &draw_index) in render_order.iter().enumerate() {
            let draw = &draws[draw_index];
            if draw.start_vpos >= draw.end_vpos {
                continue;
            }
            let first_second = draw.start_vpos.div_euclid(VPOSS_PER_SECOND);
            let last_second = (draw.end_vpos - 1).div_euclid(VPOSS_PER_SECOND);
            events.push(Event {
                second: first_second,
                rank,
                entering: true,
            });
            events.push(Event {
                second: last_second + 1,
                rank,
                entering: false,
            });
        }
        events.sort_by_key(|event| (event.second, event.entering, event.rank));

        Self {
            render_order,
            events,
            event_cursor: 0,
            current_second: None,
            active: BTreeSet::new(),
            cached: VecDeque::with_capacity(MAX_CACHED_SECONDS),
        }
    }

    /// Replace the indexed draw set and invalidate all cached second buckets.
    pub fn rebuild(&mut self, draws: &[Draw]) {
        *self = Self::new(draws);
    }

    /// Return draw indices which overlap the one-second bucket containing vpos.
    /// The shader still checks each draw's exact half-open interval per frame.
    pub fn candidate_draw_indices(&mut self, vpos: i32) -> Vec<usize> {
        let second = vpos.div_euclid(VPOSS_PER_SECOND);
        if let Some(position) = self.cached.iter().position(|(key, _)| *key == second) {
            let entry = self.cached.remove(position).expect("cache position exists");
            let result = entry.1.clone();
            self.cached.push_back(entry);
            return result;
        }

        if self.current_second.is_some_and(|current| second < current) {
            self.active.clear();
            self.event_cursor = 0;
        }
        while self.event_cursor < self.events.len()
            && self.events[self.event_cursor].second <= second
        {
            let event = self.events[self.event_cursor];
            if event.entering {
                self.active.insert(event.rank);
            } else {
                self.active.remove(&event.rank);
            }
            self.event_cursor += 1;
        }
        self.current_second = Some(second);

        let candidates: Vec<usize> = self
            .active
            .iter()
            .map(|&rank| self.render_order[rank])
            .collect();
        self.cached.push_back((second, candidates.clone()));
        if self.cached.len() > MAX_CACHED_SECONDS {
            self.cached.pop_front();
        }
        candidates
    }

    pub fn cached_seconds(&self) -> usize {
        self.cached.len()
    }
}

pub fn is_visible_at(draw: &Draw, vpos: i32) -> bool {
    draw.start_vpos <= vpos && vpos < draw.end_vpos
}

/// Group adjacent draws that use the same texture without changing their order.
/// Each input tuple is (owner order, comment index, texture id).
pub fn contiguous_texture_runs(order: &[(u32, u32, u32)]) -> Vec<(u32, usize, usize)> {
    let mut runs = Vec::new();
    let Some((_, _, first_asset)) = order.first().copied() else {
        return runs;
    };
    let mut current_asset = first_asset;
    let mut start = 0;
    for (index, &(_, _, asset_id)) in order.iter().enumerate().skip(1) {
        if asset_id != current_asset {
            runs.push((current_asset, start, index));
            current_asset = asset_id;
            start = index;
        }
    }
    runs.push((current_asset, start, order.len()));
    runs
}

#[cfg(test)]
mod tests {
    use super::{contiguous_texture_runs, SceneIndex};
    use crate::protocol::Draw;

    fn draw(owner_order: u32, comment_index: u32, primitive_index: u32) -> Draw {
        Draw {
            asset_id: primitive_index,
            start_vpos: 0,
            end_vpos: 100,
            anchor_vpos: 0,
            owner_order,
            comment_index,
            primitive_index,
            rect: [0.0; 4],
            projection: [0.0; 16],
            alpha: 1.0,
            anchor_x: 0.0,
            speed_x: 0.0,
        }
    }

    #[test]
    fn empty_scene_has_no_texture_runs() {
        assert!(contiguous_texture_runs(&[]).is_empty());
    }

    #[test]
    fn rebuilding_scene_index_exposes_a_late_draw_in_the_same_second() {
        let mut draws = vec![draw(0, 0, 0)];
        let mut index = SceneIndex::new(&draws);
        assert_eq!(index.candidate_draw_indices(42), vec![0]);

        draws.push(draw(0, 1, 0));
        index.rebuild(&draws);
        assert_eq!(index.candidate_draw_indices(42), vec![0, 1]);
    }

    #[test]
    fn rebuilding_index_sorts_late_draws_by_owner_comment_and_primitive() {
        let draws = vec![draw(1, 0, 0), draw(0, 1, 1), draw(0, 1, 0), draw(0, 0, 0)];
        let mut index = SceneIndex::new(&draws);
        assert_eq!(
            index.candidate_draw_indices(42),
            vec![3, 2, 1, 0],
            "candidate order uses owner, comment, primitive, stable index"
        );
    }
}
