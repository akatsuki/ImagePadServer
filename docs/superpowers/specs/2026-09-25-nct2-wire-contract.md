# NCT2 wire contract

**Status:** T5.1 contract updated on 2026-09-27 for execution-plan v136 and the A2 digest-feasibility follow-up.
**Scope:** Define the opt-in NCT2 stdin stream and independent golden fixtures. This does not change NCT1, renderer code, FFmpeg settings, GPU APIs, or production selection.

## 1. Invariants

- NCT1 remains a complete, validated scene before rendering. NCT2 is a separate protocol selected only by the helper's --stdin-stream option.
- Resolve the complete comment layout, supported-feature checks, and each comment's half-open visible interval before extracting any image. Freeze the resulting owner/index order. Do not reorder comments by time.
- NCT2 can write frames only to private staging output. A safe frame prefix proves that the not-yet-extracted suffix cannot affect those frames; it does not prove that the whole export is valid or publishable.
- Preserve the existing NCT1 normalized scene: bundle digest, stable asset IDs and pixels, and every 128-byte Draw in owner/index/primitive order.
- FrameClock comparisons use exact integer arithmetic. All ranges are half-open.

## 2. Primitive encoding

Every integer is little-endian. Signed integers use two's-complement. Float fields are finite IEEE-754 binary32 or binary64 in little-endian order. There is no native-endian or variable-width encoding.

Each record starts with a 16-byte envelope. Offsets are relative to the first envelope byte.

| Offset | Type | Meaning |
|---:|---|---|
| 0 | u32 | Record type |
| 4 | u32 | Payload byte length; excludes the 16-byte envelope |
| 8 | u64 | Sequence, beginning at 0 and increasing by exactly 1 |

Sequence covers every record, including Header and End. Reject a missing, repeated, decreasing, or overflowing sequence. Reject an unknown record type, a payload above 16 MiB, a truncated record, or a length that would overflow before allocating its payload. End must be the last record; any byte after it is invalid.

| Type | Record |
|---:|---|
| 1 | Header |
| 2 | Declarations |
| 3 | DeclarationsComplete |
| 4 | AssetBegin |
| 5 | AssetChunk |
| 6 | AssetEnd |
| 7 | ElementComplete |
| 8 | Watermark |
| 9 | End |

The payload length of each fixed record below is exact. A receiver rejects extra fields, nonzero reserved bytes, and unsupported flag bits.

The record order is fixed: one Header, one Declarations, one DeclarationsComplete, then the body records, one End, and EOF. AssetBegin/AssetChunk/AssetEnd form one non-interleaved transfer at a time; chunks and the end marker must belong to that open asset. An ElementComplete may follow only after all of its referenced assets are complete. ElementComplete ordinals must be exactly 0, 1, 2, and so on. Watermark may occur only after DeclarationsComplete and with no asset transfer open. No declaration record is legal after DeclarationsComplete.

## 3. Header

Header is sequence 0 and has exactly 80 payload bytes. This carries output metadata but no asset/draw totals, because those totals are not known until extraction completes.

| Payload offset | Type | Meaning |
|---:|---|---|
| 0 | 4 bytes | ASCII NCT2 |
| 4 | u32 | Header bytes, exactly 80 |
| 8 | u32 | Protocol version, exactly 2 |
| 12 | u32 | Output width |
| 16 | u32 | Output height |
| 20 | u32 | Frame count N |
| 24 | u32 | FPS numerator |
| 28 | u32 | FPS denominator |
| 32 | u32 | Declaration count, at most 100,000 |
| 36 | u32 | Flags, exactly 3: premultiplied RGBA8 and top-left row origin |
| 40 | 32 bytes | SHA-256 of the pinned niconicomments bundle |
| 72 | u32 | Reserved, zero |
| 76 | u32 | Reserved, zero |

Width, height, frame count, FPS, and flag constraints match NCT1. In particular, frame count is 1..1,000,000, dimensions are within the NCT1 bounds, FPS numerator and denominator are each at most 1,000,000, and the positive rational FPS is no greater than 60. Header metadata is validated before any body allocation.

Define the exact frame clock for integer frame f as:

    FrameClock(f) = floor(f * FPSDen * 100 / FPSNum)

For the NCT2 output's N frames, define the exclusive output end:

    E = FrameClock(N)

Compute this with checked integer arithmetic, not floating point. With the supported FPS range, every output frame 0 <= f < N satisfies FrameClock(f) < E.

## 4. Frozen declarations and clipping

Declarations lists every eligible comment in the already-frozen owner/index order. It is one record: the maximum 100,000 entries at 20 bytes each plus its four-byte count is below the 16 MiB payload limit, so segmentation is unnecessary.

The Declarations payload is count:u32 followed by count entries of exactly 20 bytes:

| Entry offset | Type | Meaning |
|---:|---|---|
| 0 | u32 | Ordinal, exactly 0 through count-1 |
| 4 | u32 | Owner order |
| 8 | u32 | Comment index |
| 12 | i32 | Clipped visible start vpos |
| 16 | i32 | Clipped visible end vpos, exclusive |

Owner order and comment index are strictly lexicographically increasing; neither pair can repeat. The original visible interval is half-open [start,end). For the readiness declaration, clip each endpoint independently to [0,E]:

    clip(v) = min(E, max(0, v))

The wire entry is [clip(start), clip(end)). It may be empty. Draw records retain the original un-clipped interval, anchor, and motion values so NCT1 normalization and rendering semantics do not change. A Draw's clipped interval must equal its declaration's clipped interval.

DeclarationsComplete is required even when the count is zero. Its 36-byte payload is count:u32 followed by SHA256(entries), where entries means the exact concatenation of the 20-byte declaration entries, without the leading count. The count must match Header and Declarations. No asset, element, watermark, or frame may be accepted before this marker.

All declarations are immutable after DeclarationsComplete. The producer must finish global layout and unsupported-feature checks before writing Header. A later unsupported element fails the attempt; it cannot amend or replace the declaration table.

## 5. Assets and completed elements

### AssetBegin, AssetChunk, AssetEnd

AssetBegin has exactly 52 payload bytes: asset ID u32 at offset 0, width u32 at 4, height u32 at 8, raw RGBA byte length u64 at 12, and expected SHA-256 at 20. IDs are unique. Dimensions are positive and at most 16,384 each. The byte length must equal width * height * 4, checked in u64 before allocation, and must not exceed 128 MiB per asset.

AssetChunk has asset ID u32 at offset 0, contiguous offset u64 at 4, and raw premultiplied RGBA8 at offset 12. Every non-final chunk carries exactly 4 MiB; the final chunk carries the remaining 1..4 MiB. Therefore, an asset of at most 4 MiB uses one final chunk, which may be small (for example, the 4-byte pixel assets in the parity fixture). Its payload length determines the data length. The next offset must equal the number of bytes already accepted for this asset; gaps, overlaps, duplicates, and overflow fail. The scene total remains at most 256 MiB.

AssetEnd has exactly 44 payload bytes: asset ID u32, received byte count u64, and computed SHA-256. All three values must match AssetBegin and the bytes received. Only then may an element reference the asset. A later duplicate begin for the same ID is invalid.

At End, every transferred asset must have been referenced by a Draw. Asset order on the wire may follow first use; normalized NCT1 assets are sorted by ID.

### ElementComplete

ElementComplete is one atomic record per ordinal, in ordinal order. Its payload is ordinal:u32, drawCount:u32, then exactly drawCount 128-byte Draw records. Payload length must equal 8 + 128 * drawCount; the per-element limit is 1,024 and total Draw count is at most 100,000.

A Draw uses the existing NCT1 128-byte layout unchanged:

| Draw offset | Type | Meaning |
|---:|---|---|
| 0 | u32 | Asset ID |
| 4 | i32 | Original visible start vpos |
| 8 | i32 | Original visible end vpos, exclusive |
| 12 | i32 | Anchor vpos |
| 16 | u32 | Owner order |
| 20 | u32 | Comment index |
| 24 | u32 | Primitive index |
| 28 | 4 × f32 | Rectangle |
| 44 | 16 × f32 | Projection matrix |
| 108 | f32 | Alpha |
| 112 | f64 | Anchor X |
| 120 | f64 | Horizontal speed |

For each Draw, owner/index must match the declaration for its ordinal; all referenced assets must have passed AssetEnd; all scalar values must satisfy NCT1 validation. Draws within one element must have primitive indices 0..drawCount-1, in order, and the clipped interval must match the declaration. An element cannot be extended by a later record.

drawCount = 0 is a valid, explicit completion for an element with no drawable primitives. It advances the completion prefix only after this complete record is read and validated. Absence of the record, even for a zero-primitive comment, does not advance anything.

An element is complete only after its complete ElementComplete payload and every referenced asset have passed all length, ordering, numeric, and hash checks. A received AssetBegin or final chunk alone is never element completion.

## 6. Watermark and safety proof

Let D be the immutable declaration array and k be the number of consecutively completed elements, so only D[0..k) is complete. The reader derives:

    ready(k) = min(D[i].clippedStart for i in [k,count)); E if k == count

Do not use the next owner's start as the minimum. A Watermark has exactly 12 payload bytes: completed prefix u32 at offset 0 and exclusive ready vpos i64 at offset 4. It is optional while progressing; the initial pair `(0,ready(0))` is implicit and computed from the declarations. If present, the first Watermark must advance beyond prefix 0, and every later Watermark must strictly advance beyond the prior Watermark prefix. Each prefix must equal the reader's current contiguous completion prefix, and its ready value must equal the recomputed value. A same-ready prefix increase is valid; ready may never decrease. A serialized no-progress Watermark at prefix 0 is invalid. An empty declaration list needs no Watermark; End carries the mandatory final `(count,E)` pair.

The renderer may emit frame f only when both conditions hold:

1. FrameClock(f) < ready(k) for the parser-validated prefix k.
2. The render scene actually used for f contains all Draws and verified assets from D[0..k) in the fixed owner/index/primitive order.

Parser completion and renderer application are separate states. A parser that has read a Watermark must not let the renderer use a stale one-second bundle or omit a completed Draw. T6/A3 owns the bounded synchronization that makes the second condition true.

**Safety argument:** if t = FrameClock(f) < ready(k), then for every uncompleted declaration i >= k, t < D[i].clippedStart. Its half-open visible interval therefore cannot contribute to frame f. The complete prefix can be rendered in original order with the same result as the full scene for that frame. The strict comparison is essential: when t == ready(k), an uncompleted element beginning at t is already visible and the frame must wait. Exact integer FrameClock arithmetic avoids boundary drift.

Examples with E = 300:

| Clipped starts | Prefix 0 | Prefix 1 | Prefix 2 | Prefix 3 |
|---|---:|---:|---:|---:|
| [0,200,0] | 0 | 0 | 0 | 300 |
| [0,200,100] | 0 | 100 | 100 | 300 |
| [0,0,0] | 0 | 0 | 0 | 300 |

For [0,200,0], reporting 200 after prefix 1 or E after prefix 2 is invalid. The later owner-order item at start 0 disproves both values. Lack of progress is safe and expected; the producer never reorders layout or extraction to improve it.

The proof applies to a valid stream that eventually completes. It does not grant success or publication if a later record is invalid.

## 7. End, EOF, staging, and publication

End has exactly 64 payload bytes:

| Payload offset | Type | Meaning |
|---:|---|---|
| 0 | u32 | Declaration count |
| 4 | u32 | Completed unique asset count |
| 8 | u32 | Total Draw count |
| 12 | u64 | Total raw asset bytes |
| 20 | u32 | Final completed prefix, exactly declaration count |
| 24 | i64 | Final ready vpos, exactly E |
| 32 | 32 bytes | NCT2 Scene Commitment v1 SHA-256 |

The End digest is a bounded-streaming scene commitment, not a SHA-256 of the full NCT1 byte stream. Its preimage is the exact concatenation:

    ASCII "NCT2-SCENE-DIGEST-v1" || 0x00 || H || descriptors || Draws

`H` is the final canonical 80-byte NCT1 header defined below, including final counts, total raw asset bytes, and flags. `descriptors` is one 52-byte descriptor per asset sorted by asset ID: `id:u32, width:u32, height:u32, rawLength:u64, verifiedPixelSHA256[32]`, all integers little-endian. `Draws` is the exact 128-byte NCT1 Draw record for every Draw in frozen ordinal/primitive order. The commitment excludes NCT2 envelopes, chunks, watermarks, End, and raw pixel bytes; each descriptor's SHA-256 binds the verified RGBA pixels. It is independent of asset transfer order and chunk boundaries.

NCT1 serialization is unchanged. The NCT1 parity fixture separately preserves the exact legacy NCT1 bytes and their full-stream SHA-256; that parity hash is not the value carried in NCT2 End.

The 80-byte NCT1 header is: magic at offset 0 (4 bytes, NCT1); header size u32 at 4; width, height, frame count, FPS numerator, FPS denominator, asset count, and Draw count as u32 at offsets 8, 12, 16, 20, 24, 28, and 32; bundle SHA-256 at 36..67; total raw asset bytes u64 at 68; and flags u32 at 76. All integers are little-endian and flags equal 3.

Each canonical NCT1 asset record is: recordBytes u32 at offset 0 (44 plus raw RGBA length, excluding this four-byte field); asset ID, width, and height u32 at offsets 4, 8, and 12; SHA-256 at 16..47; then exactly the raw RGBA bytes from offset 48. Assets are sorted by ID. After all assets, append every 128-byte Draw in frozen ordinal/primitive order. The original Draw intervals and values are preserved. The NCT1 parity golden fixes one known byte sequence and full-stream digest. Go and Rust must compute the same NCT2 Scene Commitment v1 over the preimage above. The DeclarationsComplete digest separately verifies the readiness declarations, including zero-primitive elements.

End is valid only after all Declarations are complete, every ordinal has one ElementComplete, all assets are complete and referenced, counts/byte totals/digests match, and the final watermark is (count,E). End is distinct from EOF:

- EOF before a complete valid End is failure.
- End followed by any byte, even one trailing byte, is failure.
- End with the pipe still open is incomplete; apply the configured no-progress deadline and fail if clean EOF does not arrive.
- The producer closes helper stdin after writing End. The reader continues checking for EOF even after all frames have been rendered.

Every emitted frame is sequential exactly once, and the final count must equal N. Bytes from the helper are RGBA only on stdout; status and reports use the existing stderr/report channel.

Frames and files remain in private staging. Publication requires all of: valid End, clean EOF, N sequential frames, helper exit 0, FFmpeg exit 0, and the configured MP4/HLS validation. A safe prefix, End alone, or an output file that merely exists is insufficient. On any later stream/process/file failure, discard this attempt and preserve prior completed outputs. Retry once from the original snapshot through the existing CPU fallback, with the existing notification; cancellation has no fallback. NCT1 behavior is unchanged.

## 8. Limits and rejection rules

Retain the existing NCT1 bounds: 3,840×2,160 output, 16,384 maximum asset dimension, 10,000 assets, 100,000 Draws, 128 MiB per asset, and 256 MiB total raw asset bytes. NCT2 adds at most 100,000 declarations, 1,024 Draws per element, 16 MiB maximum payload per record, and 4 MiB maximum raw data per AssetChunk. Go remains capped at 512 MiB, Rust CPU at 256 MiB, and GPU at 768 MiB; the total application-managed payload remains 1.5 GiB, with two 4 MiB input chunk credits. Reserve the relevant credit before allocating. Do not turn queue bounds into an unbounded buffer.

Reject before use/allocation: wrong magic/version/header size/flags, nonzero reserved bytes, invalid or out-of-order declarations, missing DeclarationsComplete, bad declaration digest, unknown or duplicate asset ID, invalid dimensions/length, noncontiguous or oversized chunks, bad asset digest, element ordinal gaps/duplicates/reordering, owner/index/interval mismatch, invalid Draw or primitive order, unknown asset reference, wrong watermark prefix/value, overflow, unknown record type, truncation, excessive length/count/bytes, End mismatch, missing End/EOF, or trailing bytes.

## 9. Hand-authored vectors

internal/nicorender/testdata/timeline/stream/ contains independent static NCT2 byte streams, a Python standard-library reference generator/checker, and vectors.json.

- nct1-compat-multidraw.nct2 normalizes byte-for-byte to the existing 33×19, three-frame NCT1 fixture. It checks two assets, signed negative starts, two Draws, float encodings, clipping, declared owner/index order, and the End scene commitment. The exact NCT1 byte stream and its full-stream SHA-256 remain separate parity evidence.
- owner-time-inversion.nct2 declares starts [0,200,0] with explicit zero-Draw ElementComplete records and watermarks (1,0), (2,0), (3,300). It prevents treating the next element's start as the suffix minimum.
- empty-scene.nct2 has zero declarations/assets/Draws, but still requires declarations completion, final (0,E), End, and EOF.
- vectors.json lists the [0,200,100], all-equal, exact-start/exact-end, negative-start, and 60000/1001 frame-clock expectations plus rejection cases for wrong watermarks and End/EOF failures.

generate_goldens.py --write explicitly regenerates fixtures. Without --write, it verifies the checked-in NCT1 source golden, fixed empty/parity/owner-order scene commitments, commitment sensitivity and asset-order invariance, all NCT2 bytes, exact normalized-NCT1 parity digests, and goldens.json; it never writes. The generator is not used by the application writer or parser. T5.2/T5.3 will consume the fixed .nct2 bytes and must not regenerate them as part of tests.
