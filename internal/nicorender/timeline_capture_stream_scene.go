package nicorender

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
)

// timelineCapturedSceneAssembler carries the legacy NCT1 texture dedupe state
// across comments while producing Draws one element at a time.
type timelineCapturedSceneAssembler struct {
	header             TimelineHeader
	assetByFingerprint map[string]uint32
	textureToAsset     map[uint32]uint32
	assetIDs           map[uint32]TimelineAsset
	nextAssetID        uint32
	drawCount          int
}

func newTimelineCapturedSceneAssembler(header TimelineHeader) *timelineCapturedSceneAssembler {
	return &timelineCapturedSceneAssembler{
		header:             header,
		assetByFingerprint: make(map[string]uint32),
		textureToAsset:     make(map[uint32]uint32),
		assetIDs:           make(map[uint32]TimelineAsset),
		nextAssetID:        1,
	}
}

// addElement returns this element's primitive-ordered Draws and only assets
// newly assigned while visiting those Draws. Callers must submit elements in
// frozen owner/index order, matching the legacy collect path.
func (assembler *timelineCapturedSceneAssembler) addElement(element timelineCapturedElement, textures map[uint32]timelineCaptureTexture) ([]TimelineDraw, []TimelineAsset, error) {
	if element.Meta.StartVPos >= element.Meta.EndVPos || len(element.Samples) == 0 || len(element.Samples) > 3 {
		return nil, nil, fmt.Errorf("niconico timeline: malformed capture for comment %d", element.Meta.Index)
	}
	for _, sample := range element.Samples {
		for _, textureID := range commandTextureIDs(sample.Commands) {
			if _, ok := textures[textureID]; !ok {
				return nil, nil, fmt.Errorf("niconico timeline: comment %d references missing texture %d", element.Meta.Index, textureID)
			}
		}
	}
	baseIndex := -1
	for i, sample := range element.Samples {
		if len(sample.Commands) > 0 {
			baseIndex = i
			break
		}
	}
	if baseIndex < 0 {
		for _, sample := range element.Samples {
			if !finite64(sample.X) || !finite64(sample.Y) {
				return nil, nil, timelineUnsupported(fmt.Sprintf("comment %d has non-finite position", element.Meta.Index))
			}
			if sample.X < float64(assembler.header.Width) && sample.X+element.Meta.Width > 0 &&
				sample.Y < float64(assembler.header.Height) && sample.Y+element.Meta.Height > 0 {
				return nil, nil, timelineUnsupported(fmt.Sprintf("comment %d is visible but emitted no image primitives", element.Meta.Index))
			}
		}
		return nil, nil, nil // valid comment entirely outside the viewport at sampled vpos values
	}

	base := element.Samples[baseIndex]
	if !finite64(base.X) || !finite64(base.Y) {
		return nil, nil, timelineUnsupported(fmt.Sprintf("comment %d has non-finite position", element.Meta.Index))
	}
	if len(base.Commands) > timelineCaptureDrawsPerItem {
		return nil, nil, timelineUnsupported(fmt.Sprintf("comment %d emits too many primitives", element.Meta.Index))
	}
	var speed float64
	motionCount := 0
	for _, sample := range element.Samples {
		if sample.VPos == base.VPos {
			continue
		}
		if !finite64(sample.X) || !finite64(sample.Y) {
			return nil, nil, timelineUnsupported(fmt.Sprintf("comment %d has non-finite probe position", element.Meta.Index))
		}
		deltaV := float64(sample.VPos - base.VPos)
		candidate := (sample.X - base.X) / deltaV
		if motionCount == 0 {
			speed = candidate
		} else if !nearTimeline64(speed, candidate, 1e-7) {
			return nil, nil, timelineUnsupported(fmt.Sprintf("comment %d X motion is not affine", element.Meta.Index))
		}
		motionCount++
	}
	if element.Meta.SpeedX != nil {
		if !finite64(*element.Meta.SpeedX) {
			return nil, nil, timelineUnsupported(fmt.Sprintf("comment %d has non-finite declared X speed", element.Meta.Index))
		}
		if motionCount > 0 && !nearTimeline64(speed, *element.Meta.SpeedX, 1e-7) {
			return nil, nil, timelineUnsupported(fmt.Sprintf("comment %d declared X speed disagrees with position probes", element.Meta.Index))
		}
		if motionCount == 0 {
			speed = *element.Meta.SpeedX
		}
	}
	if element.Meta.Loc == "naka" {
		if motionCount == 0 && element.Meta.SpeedX == nil {
			return nil, nil, timelineUnsupported(fmt.Sprintf("comment %d has neither position probes nor a declared X speed", element.Meta.Index))
		}
		if speed > 1e-7 {
			return nil, nil, timelineUnsupported(fmt.Sprintf("comment %d moves right without an explicitly supported reverse effect", element.Meta.Index))
		}
	} else if (motionCount > 0 || element.Meta.SpeedX != nil) && math.Abs(speed) > 1e-7 {
		return nil, nil, timelineUnsupported(fmt.Sprintf("fixed comment %d moved between probes", element.Meta.Index))
	}
	visibleStart, visibleEnd, visible, err := clipTimelineStageVisibility(
		element.Meta, base, speed, assembler.header.Width, assembler.header.Height,
	)
	if err != nil {
		return nil, nil, timelineUnsupported(fmt.Sprintf("comment %d stage visibility: %v", element.Meta.Index, err))
	}
	if !visible {
		return nil, nil, nil
	}

	baseCommands := base.Commands
	draws := make([]TimelineDraw, 0, len(baseCommands))
	newAssets := make([]TimelineAsset, 0, len(baseCommands))
	newAssetIDs := make(map[uint32]struct{}, len(baseCommands))
	for primitiveIndex, command := range baseCommands {
		if command.ID == 0 {
			return nil, nil, timelineUnsupported(fmt.Sprintf("comment %d emits a rectangle or unknown primitive", element.Meta.Index))
		}
		texture, ok := textures[command.ID]
		if !ok || texture.Width == 0 || texture.Height == 0 || uint64(len(texture.RGBA)) != uint64(texture.Width)*uint64(texture.Height)*4 {
			return nil, nil, fmt.Errorf("niconico timeline: invalid captured texture %d", command.ID)
		}
		assetID, exists := assembler.textureToAsset[command.ID]
		if !exists {
			fingerprint := fmt.Sprintf("%d:%d:%s", texture.Width, texture.Height, hex.EncodeToString(texture.SHA256[:]))
			if existingID, found := assembler.assetByFingerprint[fingerprint]; found {
				existing := assembler.assetIDs[existingID]
				if bytes.Equal(existing.RGBA, texture.RGBA) {
					assetID = existingID
				} else {
					fingerprint += ":collision"
				}
			}
			if assetID == 0 {
				if len(assembler.assetIDs) >= MaxTimelineAssets || assembler.nextAssetID == 0 {
					return nil, nil, timelineUnsupported("asset count exceeds the NCT1 bound")
				}
				assetID = assembler.nextAssetID
				assembler.nextAssetID++
				asset := TimelineAsset{ID: assetID, Width: texture.Width, Height: texture.Height, SHA256: texture.SHA256, RGBA: texture.RGBA}
				assembler.assetIDs[assetID] = asset
				assembler.assetByFingerprint[fingerprint] = assetID
				if _, alreadyNew := newAssetIDs[assetID]; !alreadyNew {
					newAssets = append(newAssets, asset)
					newAssetIDs[assetID] = struct{}{}
				}
			}
			assembler.textureToAsset[command.ID] = assetID
		}
		for _, sample := range element.Samples {
			if len(sample.Commands) == 0 {
				continue
			}
			if len(sample.Commands) != len(baseCommands) {
				return nil, nil, timelineUnsupported(fmt.Sprintf("comment %d primitive count changed between probes", element.Meta.Index))
			}
			probe := sample.Commands[primitiveIndex]
			probeTexture, ok := textures[probe.ID]
			if !ok || !sameTimelineImage(texture, probeTexture) || !sameTimelinePrimitiveExceptX(command, probe) {
				return nil, nil, timelineUnsupported(fmt.Sprintf("comment %d primitive %d changed image, projection, size, or alpha", element.Meta.Index, primitiveIndex))
			}
			deltaX := float64(probe.Rect[0] - command.Rect[0])
			expectedX := sample.X - base.X
			if math.Abs(deltaX-expectedX) > 1.0/256.0 {
				return nil, nil, timelineUnsupported(fmt.Sprintf("comment %d primitive %d did not follow affine comment X", element.Meta.Index, primitiveIndex))
			}
		}
		if assembler.drawCount >= MaxTimelineDraws {
			return nil, nil, timelineUnsupported("primitive count exceeds the NCT1 draw bound")
		}
		if !finite32(command.Color[0]) || command.Color[0] < 0 || command.Color[0] > 1 {
			return nil, nil, timelineUnsupported(fmt.Sprintf("comment %d primitive %d has invalid alpha", element.Meta.Index, primitiveIndex))
		}
		for _, value := range command.Rect {
			if !finite32(value) {
				return nil, nil, timelineUnsupported(fmt.Sprintf("comment %d primitive %d has non-finite rectangle", element.Meta.Index, primitiveIndex))
			}
		}
		for _, value := range command.Proj {
			if !finite32(value) {
				return nil, nil, timelineUnsupported(fmt.Sprintf("comment %d primitive %d has non-finite projection", element.Meta.Index, primitiveIndex))
			}
		}
		draw := TimelineDraw{
			AssetID: assetID, StartVPos: visibleStart, EndVPos: visibleEnd,
			AnchorVPos: base.VPos, OwnerOrder: element.Meta.OwnerOrder, CommentIndex: element.Meta.Index,
			PrimitiveIndex: uint32(primitiveIndex), Rect: command.Rect, Projection: command.Proj,
			Alpha: command.Color[0], AnchorX: base.X, SpeedX: speed,
		}
		draws = append(draws, draw)
		assembler.drawCount++
	}
	return draws, newAssets, nil
}

func (assembler *timelineCapturedSceneAssembler) finish() ([]TimelineAsset, error) {
	usedIDs := make([]uint32, 0, len(assembler.assetIDs))
	for id := range assembler.assetIDs {
		usedIDs = append(usedIDs, id)
	}
	sort.Slice(usedIDs, func(i, j int) bool { return usedIDs[i] < usedIDs[j] })
	assets := make([]TimelineAsset, 0, len(usedIDs))
	for _, id := range usedIDs {
		assets = append(assets, assembler.assetIDs[id])
	}
	return assets, nil
}
