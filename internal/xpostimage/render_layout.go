package xpostimage

import (
	"fmt"
	"image"
)

const (
	layoutBlockGap       = 28
	layoutFrameInset     = 32
	layoutFrameTopInset  = 48
	layoutContentInset   = 64
	compositionSideInset = 64
	textOnlySideInset    = 112
	stackedPostSideInset = 48
	sidePostSideInset    = 24
	spacerSideInset      = 96
	sideImageBlockWidth  = 800
	postIDBlockHeight    = 136
	quotedIDBlockHeight  = 88
	quoteBlockPadding    = layoutBlockGap
	quoteLabelHeight     = 20
	postCardBlockHeight  = 182
	textCardBlockHeight  = 408
	sideCardBlockHeight  = 350
	spacerLineThickness  = 3
)

type quotedPostBlockLayout struct {
	Bounds  image.Rectangle
	ID      image.Rectangle
	Content image.Rectangle
	Media   image.Rectangle
}

func quotedPostBlockLayoutFor(width, idHeight, bodyHeight, mediaWidth, mediaHeight int) quotedPostBlockLayout {
	idTop := quoteBlockPadding + quoteLabelHeight + layoutBlockGap
	contentTop := idTop + idHeight + layoutBlockGap
	left := quoteBlockPadding
	right := width - quoteBlockPadding
	media := image.Rectangle{}
	if mediaWidth > 0 && mediaHeight > 0 {
		mediaLeft := right - mediaWidth
		media = image.Rect(mediaLeft, idTop, right, idTop+mediaHeight)
		right = mediaLeft - layoutBlockGap
	}
	contentBottom := contentTop + bodyHeight
	rowBottom := max(idTop+idHeight, contentBottom, media.Max.Y)
	height := rowBottom + quoteBlockPadding
	return quotedPostBlockLayout{
		Bounds:  image.Rect(0, 0, width, height),
		ID:      image.Rect(left, idTop, right, idTop+idHeight),
		Content: image.Rect(left, contentTop, right, contentBottom),
		Media:   media,
	}
}

func arrangeCenteredColumn(canvas image.Point, blocks []image.Point, gap int) ([]image.Rectangle, image.Rectangle) {
	if len(blocks) == 0 {
		return nil, image.Rectangle{}
	}
	width := 0
	height := gap * (len(blocks) - 1)
	for _, block := range blocks {
		width = max(width, block.X)
		height += block.Y
	}
	left := (canvas.X - width) / 2
	groupTop := (canvas.Y - height) / 2
	top := groupTop
	rects := make([]image.Rectangle, len(blocks))
	for index, block := range blocks {
		x := left + (width-block.X)/2
		rects[index] = image.Rect(x, top, x+block.X, top+block.Y)
		top += block.Y + gap
	}
	return rects, image.Rect(left, groupTop, left+width, groupTop+height)
}

func arrangeCenteredRow(canvas image.Point, blocks []image.Point, gap int) ([]image.Rectangle, image.Rectangle) {
	if len(blocks) == 0 {
		return nil, image.Rectangle{}
	}
	width := gap * (len(blocks) - 1)
	height := 0
	for _, block := range blocks {
		width += block.X
		height = max(height, block.Y)
	}
	groupLeft := (canvas.X - width) / 2
	top := (canvas.Y - height) / 2
	left := groupLeft
	rects := make([]image.Rectangle, len(blocks))
	for index, block := range blocks {
		y := top + (height-block.Y)/2
		rects[index] = image.Rect(left, y, left+block.X, y+block.Y)
		left += block.X + gap
	}
	return rects, image.Rect(groupLeft, top, groupLeft+width, top+height)
}

func translateRect(rect image.Rectangle, offset image.Point) image.Rectangle {
	return rect.Add(offset)
}

func fitStackedImageAndContent(maxGroupHeight, fixedPostHeight, naturalImageHeight, gap, spacerHeight, minimumBodyHeight int, measureBody func(maxHeight int) (int, error)) (int, int, error) {
	availableHeight := maxGroupHeight - fixedPostHeight - 2*gap - spacerHeight
	if availableHeight < minimumBodyHeight {
		return 0, 0, fmt.Errorf("post block leaves no room for its content and image")
	}
	maxImageHeight := min(max(0, naturalImageHeight), availableHeight-minimumBodyHeight)
	bodyHeight, err := measureBody(availableHeight)
	if err != nil {
		return 0, 0, err
	}
	bestImageHeight, bestBodyHeight := 0, bodyHeight
	low, high := 1, maxImageHeight
	for low <= high {
		candidate := low + (high-low)/2
		candidateBodyHeight, measureErr := measureBody(availableHeight - candidate)
		if measureErr == nil {
			bestImageHeight, bestBodyHeight = candidate, candidateBodyHeight
			low = candidate + 1
		} else {
			high = candidate - 1
		}
	}
	return bestImageHeight, bestBodyHeight, nil
}

func frameBounds(content image.Rectangle, padding int) image.Rectangle {
	return frameBoundsWithInsets(content, padding, padding, padding, padding)
}

func frameBoundsWithInsets(content image.Rectangle, left, top, right, bottom int) image.Rectangle {
	if content.Empty() || left < 0 || top < 0 || right < 0 || bottom < 0 {
		return image.Rectangle{}
	}
	return image.Rect(content.Min.X-left, content.Min.Y-top, content.Max.X+right, content.Max.Y+bottom)
}
