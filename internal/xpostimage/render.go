package xpostimage

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	_ "time/tzdata"
	"unicode"
	"unicode/utf8"

	"github.com/fogleman/gg"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
)

type fontSource struct {
	font *opentype.Font
}

var tweetURLPattern = regexp.MustCompile(`(?i)(?:https?://|www\.)[^\s<>"'「」『』]+|(?:[a-z0-9-]+\.)+[a-z]{2,}(?:/[^\s<>"'「」『』]*)?`)
var tweetSpacePattern = regexp.MustCompile(`[\t \x{3000}]+`)
var tweetSpaceBeforePunctuationPattern = regexp.MustCompile(`[\t ]+([,.;:!?、。，！？])`)
var tweetStatusMediaPattern = regexp.MustCompile(`(?i)/(?:photo|video)/\d+/?$`)

const imageCornerRadius = 20
const imageGalleryGap = 12

type tweetURLSpan struct {
	start   int
	end     int
	display string
	link    *postLink
}

func renderPost(post postData, photo, avatar, cardImage, quotedAvatar image.Image, themeMode, fontPath string) (*image.RGBA, error) {
	mediaImages := post.MediaImages
	if len(mediaImages) == 0 && photo != nil {
		mediaImages = []image.Image{photo}
	}
	// Resolve image count, arrangement, and canvas orientation before measuring
	// any post blocks. The remaining layout is built around this image block.
	layout := chooseMediaLayout(post.Photo, photoInfoForImages(mediaImages))
	palette, err := paletteForTheme(themeMode)
	if err != nil {
		return nil, err
	}
	cardSourceURL := ""
	if post.Card != nil {
		cardSourceURL = post.Card.SourceURL
		if cardSourceURL == "" {
			cardSourceURL = post.Card.URL
		}
	}
	fonts, err := loadFontSource(fontPath)
	if err != nil {
		return nil, err
	}
	dc := gg.NewContext(layout.width, layout.height)
	dc.SetHexColor(palette.Background)
	dc.Clear()

	postWidth, postInset, mediaWidth := 0, 0, 0
	fontMaxSize, fontMinSize := 48, 18
	switch layout.kind {
	case layoutTextOnly:
		postWidth = layout.width - 2*textOnlySideInset
		fontMaxSize, fontMinSize = 58, 30
	case layoutStacked:
		postWidth = layout.width - 2*compositionSideInset
		postInset = stackedPostSideInset
		mediaWidth = layout.width - 2*compositionSideInset
	case layoutSideBySide:
		mediaWidth = sideImageBlockWidth
		postWidth = layout.width - 2*compositionSideInset - mediaWidth - 2*layoutBlockGap - spacerLineThickness
		postInset = sidePostSideInset
		fontMinSize = 26
	default:
		return nil, fmt.Errorf("unsupported layout %q", layout.kind)
	}
	bodyWidth := postWidth - 2*postInset
	if bodyWidth <= 0 {
		return nil, fmt.Errorf("post block has no available width")
	}
	maxCompositionHeight := layout.height - 2*layoutContentInset
	var quoteLayout quotedPostBlockLayout
	if post.Quoted != nil {
		quoteLayout, err = measureQuotedPostBlock(dc, fonts, post.Quoted, bodyWidth, maxCompositionHeight/2)
		if err != nil {
			return nil, err
		}
	}
	mediaHeight := 0
	if len(mediaImages) > 0 {
		mediaHeight = naturalGalleryHeight(mediaImages, mediaWidth, layout.gallery)
	}

	postCardHeight := postCardBlockHeight
	if layout.kind == layoutTextOnly {
		postCardHeight = textCardBlockHeight
	} else if layout.kind == layoutSideBySide {
		postCardHeight = sideCardBlockHeight
	}
	postFixedHeight := postIDBlockHeight + layoutBlockGap
	if post.Card != nil {
		postFixedHeight += layoutBlockGap + postCardHeight
	}
	if post.Quoted != nil {
		postFixedHeight += layoutBlockGap + quoteLayout.Bounds.Dy()
	}

	var bodyHeight int
	if layout.kind == layoutStacked && mediaHeight > 0 {
		minBodyHeight := int(math.Ceil(float64(fontMinSize) * 1.35))
		mediaHeight, bodyHeight, err = fitStackedImageAndContent(maxCompositionHeight, postFixedHeight, mediaHeight, layoutBlockGap, spacerLineThickness, minBodyHeight, func(maxHeight int) (int, error) {
			usedHeight, measureErr := measureTweetBodyHeight(dc, fonts, post.Text, post.Links, cardSourceURL, post.MediaSourceURL, float64(bodyWidth), float64(maxHeight), fontMaxSize, fontMinSize)
			return int(math.Ceil(usedHeight)), measureErr
		})
		if err != nil {
			return nil, err
		}
		if mediaHeight <= 0 {
			return nil, fmt.Errorf("post block leaves no room for the image block")
		}
	} else if layout.kind == layoutSideBySide {
		mediaHeight = min(mediaHeight, maxCompositionHeight)
	}
	if bodyHeight == 0 {
		bodyBudget := maxCompositionHeight - postFixedHeight
		if bodyBudget <= 0 {
			return nil, fmt.Errorf("post block leaves no room for its content")
		}
		usedBodyHeight, measureErr := measureTweetBodyHeight(dc, fonts, post.Text, post.Links, cardSourceURL, post.MediaSourceURL, float64(bodyWidth), float64(bodyBudget), fontMaxSize, fontMinSize)
		if measureErr != nil {
			return nil, measureErr
		}
		bodyHeight = int(math.Ceil(usedBodyHeight))
	}
	contentHeight := bodyHeight
	if post.Card != nil {
		contentHeight += layoutBlockGap + postCardHeight
	}
	postHeights := []int{postIDBlockHeight, contentHeight}
	if post.Quoted != nil {
		postHeights = append(postHeights, quoteLayout.Bounds.Dy())
	}
	postHeight := 0
	postParts := make([]image.Point, len(postHeights))
	for index, height := range postHeights {
		postParts[index] = image.Pt(bodyWidth, height)
		postHeight += height
	}
	postHeight += layoutBlockGap * (len(postHeights) - 1)
	postPartRects, _ := arrangeCenteredColumn(image.Pt(postWidth, postHeight), postParts, layoutBlockGap)

	var postBlockRect, spacerRect, imageBlockRect, compositionBounds image.Rectangle
	switch layout.kind {
	case layoutTextOnly:
		rects, bounds := arrangeCenteredColumn(image.Pt(layout.width, layout.height), []image.Point{image.Pt(postWidth, postHeight)}, 0)
		postBlockRect, compositionBounds = rects[0], bounds
	case layoutStacked:
		if mediaHeight == 0 {
			rects, bounds := arrangeCenteredColumn(image.Pt(layout.width, layout.height), []image.Point{image.Pt(postWidth, postHeight)}, 0)
			postBlockRect, compositionBounds = rects[0], bounds
			break
		}
		spacerWidth := layout.width - 2*spacerSideInset
		blocks := []image.Point{
			image.Pt(postWidth, postHeight),
			image.Pt(spacerWidth, spacerLineThickness),
			image.Pt(mediaWidth, mediaHeight),
		}
		rects, bounds := arrangeCenteredColumn(image.Pt(layout.width, layout.height), blocks, layoutBlockGap)
		postBlockRect, spacerRect, imageBlockRect, compositionBounds = rects[0], rects[1], rects[2], bounds
	case layoutSideBySide:
		groupHeight := max(postHeight, mediaHeight)
		blocks := []image.Point{
			image.Pt(mediaWidth, mediaHeight),
			image.Pt(spacerLineThickness, groupHeight),
			image.Pt(postWidth, postHeight),
		}
		rects, bounds := arrangeCenteredRow(image.Pt(layout.width, layout.height), blocks, layoutBlockGap)
		imageBlockRect, spacerRect, postBlockRect, compositionBounds = rects[0], rects[1], rects[2], bounds
	}

	idRect := translateRect(postPartRects[0], postBlockRect.Min)
	contentRect := translateRect(postPartRects[1], postBlockRect.Min)
	if err := drawIDBlock(dc, fonts, palette, post, avatar, idRect.Min.X, idRect.Min.Y, float64(idRect.Dx())); err != nil {
		return nil, err
	}
	bodyRect := image.Rect(contentRect.Min.X, contentRect.Min.Y, contentRect.Max.X, contentRect.Min.Y+bodyHeight)
	if err := drawPostContent(dc, fonts, palette, post.Text, post.Links, cardSourceURL, post.MediaSourceURL, bodyRect.Min.X, bodyRect.Min.Y, float64(bodyRect.Dx()), float64(bodyRect.Dy()), fontMaxSize, fontMinSize); err != nil {
		return nil, err
	}
	if post.Card != nil {
		cardTop := bodyRect.Max.Y + layoutBlockGap
		cardRect := image.Rect(contentRect.Min.X, cardTop, contentRect.Max.X, contentRect.Max.Y)
		if err := drawLinkCard(dc, fonts, palette, post.Card, cardImage, post.CardImageIsMain, cardRect); err != nil {
			return nil, err
		}
	}
	if post.Quoted != nil {
		quotedPostBlockRect := translateRect(postPartRects[2], postBlockRect.Min)
		if err := drawQuotedPostBlock(dc, fonts, palette, post.Quoted, quotedAvatar, quotedPostBlockRect, quoteLayout); err != nil {
			return nil, err
		}
	}
	if !spacerRect.Empty() {
		if layout.kind == layoutStacked {
			y := float64(spacerRect.Min.Y + spacerRect.Dy()/2)
			drawDivider(dc, float64(spacerRect.Min.X), y, float64(spacerRect.Max.X), y, palette.Divider)
		} else {
			x := float64(spacerRect.Min.X + spacerRect.Dx()/2)
			drawDivider(dc, x, float64(spacerRect.Min.Y), x, float64(spacerRect.Max.Y), palette.Divider)
		}
	}
	if !imageBlockRect.Empty() {
		drawArrangedImageGallery(dc, imageBlockRect, mediaImages, layout.gallery)
	}
	// The content geometry is settled and centered before the outer bevel is drawn.
	drawPostFrame(dc, frameBoundsWithInsets(compositionBounds, layoutFrameInset, layoutFrameTopInset, layoutFrameInset, layoutFrameInset), palette)

	rgba, ok := dc.Image().(*image.RGBA)
	if !ok {
		return nil, errors.New("renderer did not produce an RGBA image")
	}
	return rgba, nil
}

func measureQuotedPostBlock(dc *gg.Context, fonts *fontSource, quoted *quotedPostData, width, maxHeight int) (quotedPostBlockLayout, error) {
	if quoted == nil || width <= 2*quoteBlockPadding {
		return quotedPostBlockLayout{}, nil
	}
	innerWidth := width - 2*quoteBlockPadding
	mediaWidth := 0
	if len(quoted.MediaImages) > 0 {
		mediaWidth = min(240, innerWidth/3)
		if len(quoted.MediaImages) > 1 {
			mediaWidth = min(480, innerWidth/2)
		}
		mediaWidth = min(mediaWidth, max(0, innerWidth-layoutBlockGap-1))
	}
	bodyWidth := innerWidth
	if mediaWidth > 0 {
		bodyWidth -= mediaWidth + layoutBlockGap
	}
	if bodyWidth <= 0 {
		return quotedPostBlockLayout{}, fmt.Errorf("quoted post has no available text width")
	}
	idTop := quoteBlockPadding + quoteLabelHeight + layoutBlockGap
	bodyTop := idTop + quotedIDBlockHeight + layoutBlockGap
	maxBodyHeight := maxHeight - bodyTop - quoteBlockPadding
	if maxBodyHeight <= 0 {
		return quotedPostBlockLayout{}, fmt.Errorf("quoted post block has no available height")
	}
	mediaHeight := naturalGalleryHeight(quoted.MediaImages, mediaWidth, galleryDefault)
	mediaHeight = min(mediaHeight, max(0, maxHeight-idTop-quoteBlockPadding))
	mediaSourceURL := ""
	if quoted.Photo != nil {
		mediaSourceURL = quoted.Photo.URL
	}
	bodyHeight, err := measureTweetBodyHeight(dc, fonts, quoted.Text, quoted.Links, "", mediaSourceURL, float64(bodyWidth), float64(maxBodyHeight), 28, 16)
	if err != nil {
		return quotedPostBlockLayout{}, fmt.Errorf("measure quoted post content: %w", err)
	}
	return quotedPostBlockLayoutFor(width, quotedIDBlockHeight, int(math.Ceil(bodyHeight)), mediaWidth, mediaHeight), nil
}

func drawQuotedPostBlock(dc *gg.Context, fonts *fontSource, palette renderPalette, quoted *quotedPostData, avatar image.Image, rect image.Rectangle, layout quotedPostBlockLayout) error {
	if quoted == nil || rect.Empty() || layout.Bounds.Empty() {
		return nil
	}
	dc.SetHexColor(palette.Panel)
	dc.DrawRoundedRectangle(float64(rect.Min.X), float64(rect.Min.Y), float64(rect.Dx()), float64(rect.Dy()), 18)
	dc.Fill()
	dc.SetHexColor(palette.PanelBorder)
	dc.SetLineWidth(2)
	dc.DrawRoundedRectangle(float64(rect.Min.X), float64(rect.Min.Y), float64(rect.Dx()), float64(rect.Dy()), 18)
	dc.Stroke()

	label := "引用ポスト"
	if quoted.Relation == "retweet" {
		label = "リポスト"
	}
	if err := drawSingleLine(dc, fonts, label, float64(rect.Min.X+quoteBlockPadding), float64(rect.Min.Y+quoteBlockPadding+16), float64(rect.Dx()-2*quoteBlockPadding), 18, 16, palette.SecondaryText); err != nil {
		return err
	}

	avatarSize := 64
	idRect := translateRect(layout.ID, rect.Min)
	avatarX := idRect.Min.X
	avatarY := idRect.Min.Y
	if avatar != nil {
		dc.DrawImage(makeCircleAvatar(avatar, avatarSize), avatarX, avatarY)
	} else {
		dc.SetHexColor(palette.Placeholder)
		dc.DrawCircle(float64(avatarX+avatarSize/2), float64(avatarY+avatarSize/2), float64(avatarSize/2))
		dc.Fill()
	}
	textX := avatarX + avatarSize + 18
	textWidth := float64(idRect.Max.X - textX)
	name := strings.TrimSpace(quoted.UserName)
	if name == "" {
		name = "Unknown user"
	}
	if err := drawSingleLine(dc, fonts, name, float64(textX), float64(avatarY+27), textWidth, 25, 17, palette.PrimaryText); err != nil {
		return err
	}
	handle := strings.TrimPrefix(strings.TrimSpace(quoted.Handle), "@")
	if handle != "" {
		handle = "@" + handle
	}
	if err := drawSingleLine(dc, fonts, handle, float64(textX), float64(avatarY+51), textWidth, 18, 14, palette.SecondaryText); err != nil {
		return err
	}
	if err := drawSingleLine(dc, fonts, formatPostDate(quoted.CreatedAt), float64(textX), float64(avatarY+73), textWidth, 16, 13, palette.SecondaryText); err != nil {
		return err
	}
	contentRect := translateRect(layout.Content, rect.Min)
	bodyWidth := float64(contentRect.Dx())
	mediaSourceURL := ""
	if quoted.Photo != nil {
		mediaSourceURL = quoted.Photo.URL
	}
	if !layout.Media.Empty() {
		drawImageGallery(dc, translateRect(layout.Media, rect.Min), quoted.MediaImages)
	}
	return drawPostContent(dc, fonts, palette, quoted.Text, quoted.Links, "", mediaSourceURL, contentRect.Min.X, contentRect.Min.Y, bodyWidth, float64(contentRect.Dy()), 28, 16)
}

func loadFontSource(fontPath string) (*fontSource, error) {
	path, err := findFontPath(fontPath)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read font %q: %w", path, err)
	}
	f, err := opentype.Parse(data)
	if err != nil {
		collection, collectionErr := opentype.ParseCollection(data)
		if collectionErr != nil {
			return nil, fmt.Errorf("parse font %q: %w", path, err)
		}
		f, err = collection.Font(0)
		if err != nil {
			return nil, fmt.Errorf("read first font in %q: %w", path, err)
		}
	}
	return &fontSource{font: f}, nil
}

func findFontPath(fontPath string) (string, error) {
	candidates := []string{}
	if fontPath != "" {
		candidates = append(candidates, fontPath)
	}
	if override := os.Getenv("XPOST_FONT"); override != "" {
		candidates = append(candidates, override)
	}
	candidates = append(candidates, filepath.Join("assets", "NotoSansJP-Regular.ttf"))
	if windir := os.Getenv("WINDIR"); windir != "" {
		candidates = append(candidates,
			filepath.Join(windir, "Fonts", "NotoSansJP-Regular.ttf"),
			filepath.Join(windir, "Fonts", "NotoSansCJK-Regular.ttc"),
			filepath.Join(windir, "Fonts", "YuGothM.ttc"),
			filepath.Join(windir, "Fonts", "msgothic.ttc"),
			filepath.Join(windir, "Fonts", "GOTHIC.TTF"),
		)
	}
	candidates = append(candidates,
		"C:\\Windows\\Fonts\\NotoSansJP-Regular.ttf",
		"C:\\Windows\\Fonts\\NotoSansCJK-Regular.ttc",
		"/Library/Fonts/NotoSansJP-Regular.otf",
		"/usr/share/fonts/opentype/noto/NotoSansJP-Regular.ttf",
		"/usr/share/fonts/opentype/noto/NotoSansJP-Regular.otf",
		"/usr/share/fonts/truetype/noto/NotoSansJP-Regular.ttf",
		"/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc",
		"/usr/share/fonts/truetype/noto/NotoSansCJK-Regular.ttc",
		"C:\\Windows\\Fonts\\YuGothM.ttc",
		"C:\\Windows\\Fonts\\msgothic.ttc",
		"C:\\Windows\\Fonts\\GOTHIC.TTF",
		"/System/Library/Fonts/ヒラギノ角ゴシック W3.ttc",
		"/System/Library/Fonts/Supplemental/Arial Unicode.ttf",
		"/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
	)
	for _, path := range candidates {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, nil
		}
	}
	return "", errors.New("no supported font found; install Noto Sans JP or set XPOST_FONT")
}

func drawDivider(dc *gg.Context, x1, y1, x2, y2 float64, hexColor string) {
	dc.SetHexColor(hexColor)
	dc.SetLineWidth(3)
	dc.MoveTo(x1, y1)
	dc.LineTo(x2, y2)
	dc.Stroke()
}

func drawPostFrame(dc *gg.Context, rect image.Rectangle, palette renderPalette) {
	if rect.Empty() {
		return
	}
	x := float64(rect.Min.X)
	y := float64(rect.Min.Y)
	width := float64(rect.Dx())
	height := float64(rect.Dy())

	drawSoftPostShadow(dc, rect, palette.FrameShadow)

	// Keep a single quiet frame edge above the diffuse outer shadow.
	dc.SetHexColor(palette.PanelBorder)
	dc.SetLineWidth(3)
	dc.DrawRoundedRectangle(x, y, width, height, 28)
	dc.Stroke()
}

func drawSoftPostShadow(dc *gg.Context, rect image.Rectangle, shadow color.Color) {
	target, ok := dc.Image().(*image.RGBA)
	if !ok || rect.Empty() {
		return
	}

	const (
		sigma        = 10.0
		shadowX      = 2.0
		shadowY      = 4.0
		shadowRadius = 30.0
	)
	shadowColor := color.NRGBAModel.Convert(shadow).(color.NRGBA)
	if shadowColor.A == 0 {
		return
	}
	baseAlpha := float64(shadowColor.A) / 255
	shadowMinX := float64(rect.Min.X) + shadowX
	shadowMinY := float64(rect.Min.Y) + shadowY
	shadowMaxX := float64(rect.Max.X) + shadowX
	shadowMaxY := float64(rect.Max.Y) + shadowY
	pad := int(math.Ceil(3 * sigma))

	minX := max(target.Bounds().Min.X, int(math.Floor(shadowMinX))-pad)
	minY := max(target.Bounds().Min.Y, int(math.Floor(shadowMinY))-pad)
	maxX := min(target.Bounds().Max.X, int(math.Ceil(shadowMaxX))+pad)
	maxY := min(target.Bounds().Max.Y, int(math.Ceil(shadowMaxY))+pad)
	const gaussianScale = 1.4142135623730951

	for py := minY; py < maxY; py++ {
		for px := minX; px < maxX; px++ {
			centerX := float64(px) + 0.5
			centerY := float64(py) + 0.5
			if roundedRectDistance(centerX, centerY, float64(rect.Min.X), float64(rect.Min.Y), float64(rect.Max.X), float64(rect.Max.Y), 28) < 0 {
				continue
			}

			distance := roundedRectDistance(centerX, centerY, shadowMinX, shadowMinY, shadowMaxX, shadowMaxY, shadowRadius)
			coverage := 0.5 * math.Erfc(distance/(sigma*gaussianScale))
			sourceAlpha := baseAlpha * coverage
			if sourceAlpha < 0.002 {
				continue
			}

			offset := target.PixOffset(px, py)
			inverseAlpha := 1 - sourceAlpha
			target.Pix[offset] = uint8(math.Round(float64(shadowColor.R)*sourceAlpha + float64(target.Pix[offset])*inverseAlpha))
			target.Pix[offset+1] = uint8(math.Round(float64(shadowColor.G)*sourceAlpha + float64(target.Pix[offset+1])*inverseAlpha))
			target.Pix[offset+2] = uint8(math.Round(float64(shadowColor.B)*sourceAlpha + float64(target.Pix[offset+2])*inverseAlpha))
			target.Pix[offset+3] = uint8(math.Round(255*sourceAlpha + float64(target.Pix[offset+3])*inverseAlpha))
		}
	}
}

func roundedRectDistance(px, py, minX, minY, maxX, maxY, radius float64) float64 {
	halfWidth := (maxX - minX) / 2
	halfHeight := (maxY - minY) / 2
	centerX := minX + halfWidth
	centerY := minY + halfHeight
	qx := math.Abs(px-centerX) - (halfWidth - radius)
	qy := math.Abs(py-centerY) - (halfHeight - radius)
	outsideX := math.Max(qx, 0)
	outsideY := math.Max(qy, 0)
	return math.Hypot(outsideX, outsideY) + math.Min(math.Max(qx, qy), 0) - radius
}

func drawImageContent(dc *gg.Context, rect image.Rectangle, img image.Image) {
	if img == nil || img.Bounds().Empty() {
		return
	}

	inner := rect.Inset(8)
	if inner.Empty() {
		return
	}
	src := img.Bounds()
	scale := math.Min(float64(inner.Dx())/float64(src.Dx()), float64(inner.Dy())/float64(src.Dy()))
	w := max(1, int(math.Round(float64(src.Dx())*scale)))
	h := max(1, int(math.Round(float64(src.Dy())*scale)))
	resized := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(resized, resized.Bounds(), img, src, draw.Src, nil)
	x := inner.Min.X + (inner.Dx()-w)/2
	y := inner.Min.Y + (inner.Dy()-h)/2
	roundImageCorners(resized, imageCornerRadius)
	dc.DrawImage(resized, x, y)
}

func drawImageGallery(dc *gg.Context, rect image.Rectangle, images []image.Image) {
	visible := make([]image.Image, 0, min(len(images), 4))
	for _, img := range images {
		if img == nil || img.Bounds().Empty() {
			continue
		}
		visible = append(visible, img)
		if len(visible) == 4 {
			break
		}
	}
	if len(visible) == 0 || rect.Empty() {
		return
	}
	if len(visible) == 1 {
		drawImageContent(dc, rect, visible[0])
		return
	}

	gap := min(imageGalleryGap, max(0, rect.Dx()-2), max(0, rect.Dy()-2))
	switch len(visible) {
	case 2:
		cellWidth := (rect.Dx() - gap) / 2
		for index, img := range visible {
			x := rect.Min.X + index*(cellWidth+gap)
			drawImageContent(dc, image.Rect(x, rect.Min.Y, x+cellWidth, rect.Max.Y), img)
		}
	case 3:
		leftWidth := (rect.Dx() - gap) / 2
		rightX := rect.Min.X + leftWidth + gap
		rightWidth := rect.Max.X - rightX
		halfHeight := (rect.Dy() - gap) / 2
		drawImageContent(dc, image.Rect(rect.Min.X, rect.Min.Y, rect.Min.X+leftWidth, rect.Max.Y), visible[0])
		drawImageContent(dc, image.Rect(rightX, rect.Min.Y, rightX+rightWidth, rect.Min.Y+halfHeight), visible[1])
		drawImageContent(dc, image.Rect(rightX, rect.Min.Y+halfHeight+gap, rect.Max.X, rect.Max.Y), visible[2])
	default:
		cellWidth := (rect.Dx() - gap) / 2
		halfHeight := (rect.Dy() - gap) / 2
		for index, img := range visible {
			column := index % 2
			row := index / 2
			x := rect.Min.X + column*(cellWidth+gap)
			y := rect.Min.Y + row*(halfHeight+gap)
			drawImageContent(dc, image.Rect(x, y, x+cellWidth, y+halfHeight), img)
		}
	}
}

func photoInfoForImages(images []image.Image) []photoInfo {
	photos := make([]photoInfo, 0, min(len(images), 4))
	for _, img := range images {
		if img == nil || img.Bounds().Empty() {
			continue
		}
		bounds := img.Bounds()
		photos = append(photos, photoInfo{Width: bounds.Dx(), Height: bounds.Dy()})
		if len(photos) == 4 {
			break
		}
	}
	return photos
}

func chooseMediaLayout(fallback *photoInfo, photos []photoInfo) canvasLayout {
	if len(photos) < 2 || len(photos) > 4 {
		return chooseLayout(fallback)
	}

	portrait := canvasLayout{kind: layoutStacked, width: 1536, height: 2048}
	landscape := canvasLayout{kind: layoutSideBySide, width: 2048, height: 1536}
	type candidate struct {
		layout      canvasLayout
		mediaBounds image.Rectangle
	}
	candidates := make([]candidate, 0, 4)
	if len(photos) == 2 {
		portrait.gallery = galleryTwoColumns
		landscape.gallery = galleryTwoRows
		candidates = append(candidates,
			candidate{layout: portrait, mediaBounds: image.Rect(0, 0, 1408, 1184)},
			candidate{layout: landscape, mediaBounds: image.Rect(0, 0, 800, 1408)},
		)
	} else {
		if len(photos) == 4 {
			candidates = append(candidates,
				candidate{layout: portrait, mediaBounds: image.Rect(0, 0, 1408, 1184)},
				candidate{layout: landscape, mediaBounds: image.Rect(0, 0, 800, 1408)},
			)
		} else {
			candidates = append(candidates,
				candidate{layout: canvasLayout{kind: layoutStacked, width: 1536, height: 2048, gallery: galleryLeftOneRightTwo}, mediaBounds: image.Rect(0, 0, 1408, 1184)},
				candidate{layout: canvasLayout{kind: layoutStacked, width: 1536, height: 2048, gallery: galleryLeftTwoRightOne}, mediaBounds: image.Rect(0, 0, 1408, 1184)},
				candidate{layout: canvasLayout{kind: layoutSideBySide, width: 2048, height: 1536, gallery: galleryTopOneBottomTwo}, mediaBounds: image.Rect(0, 0, 800, 1408)},
				candidate{layout: canvasLayout{kind: layoutSideBySide, width: 2048, height: 1536, gallery: galleryTopTwoBottomOne}, mediaBounds: image.Rect(0, 0, 800, 1408)},
			)
		}
	}

	best := candidates[0]
	bestArea := -1.0
	for _, option := range candidates {
		area := galleryDisplayedArea(photos, option.mediaBounds, option.layout.gallery)
		if area > bestArea {
			best = option
			bestArea = area
		}
	}
	return best.layout
}

func galleryDisplayedArea(photos []photoInfo, bounds image.Rectangle, arrangement galleryArrangement) float64 {
	cells := galleryCellRects(bounds, arrangement, len(photos))
	if len(cells) != len(photos) {
		return 0
	}
	total := 0.0
	for index, photo := range photos {
		if photo.Width <= 0 || photo.Height <= 0 {
			continue
		}
		cell := cells[index].Inset(8)
		if cell.Empty() {
			continue
		}
		scale := math.Min(float64(cell.Dx())/float64(photo.Width), float64(cell.Dy())/float64(photo.Height))
		total += float64(photo.Width) * float64(photo.Height) * scale * scale
	}
	return total
}

func galleryCellRects(rect image.Rectangle, arrangement galleryArrangement, count int) []image.Rectangle {
	if rect.Empty() || (count != 2 && count != 3 && count != 4) {
		return nil
	}
	gap := min(imageGalleryGap, max(0, rect.Dx()-2), max(0, rect.Dy()-2))
	halfWidth := (rect.Dx() - gap) / 2
	halfHeight := (rect.Dy() - gap) / 2
	midX := rect.Min.X + halfWidth + gap
	midY := rect.Min.Y + halfHeight + gap
	if count == 4 && arrangement == galleryDefault {
		return []image.Rectangle{
			image.Rect(rect.Min.X, rect.Min.Y, rect.Min.X+halfWidth, rect.Min.Y+halfHeight),
			image.Rect(midX, rect.Min.Y, rect.Max.X, rect.Min.Y+halfHeight),
			image.Rect(rect.Min.X, midY, rect.Min.X+halfWidth, rect.Max.Y),
			image.Rect(midX, midY, rect.Max.X, rect.Max.Y),
		}
	}
	switch arrangement {
	case galleryTwoColumns:
		if count != 2 {
			return nil
		}
		return []image.Rectangle{
			image.Rect(rect.Min.X, rect.Min.Y, rect.Min.X+halfWidth, rect.Max.Y),
			image.Rect(midX, rect.Min.Y, rect.Max.X, rect.Max.Y),
		}
	case galleryTwoRows:
		if count != 2 {
			return nil
		}
		return []image.Rectangle{
			image.Rect(rect.Min.X, rect.Min.Y, rect.Max.X, rect.Min.Y+halfHeight),
			image.Rect(rect.Min.X, midY, rect.Max.X, rect.Max.Y),
		}
	case galleryLeftOneRightTwo:
		if count != 3 {
			return nil
		}
		return []image.Rectangle{
			image.Rect(rect.Min.X, rect.Min.Y, rect.Min.X+halfWidth, rect.Max.Y),
			image.Rect(midX, rect.Min.Y, rect.Max.X, rect.Min.Y+halfHeight),
			image.Rect(midX, midY, rect.Max.X, rect.Max.Y),
		}
	case galleryLeftTwoRightOne:
		if count != 3 {
			return nil
		}
		return []image.Rectangle{
			image.Rect(rect.Min.X, rect.Min.Y, rect.Min.X+halfWidth, rect.Min.Y+halfHeight),
			image.Rect(rect.Min.X, midY, rect.Min.X+halfWidth, rect.Max.Y),
			image.Rect(midX, rect.Min.Y, rect.Max.X, rect.Max.Y),
		}
	case galleryTopOneBottomTwo:
		if count != 3 {
			return nil
		}
		return []image.Rectangle{
			image.Rect(rect.Min.X, rect.Min.Y, rect.Max.X, rect.Min.Y+halfHeight),
			image.Rect(rect.Min.X, midY, rect.Min.X+halfWidth, rect.Max.Y),
			image.Rect(midX, midY, rect.Max.X, rect.Max.Y),
		}
	case galleryTopTwoBottomOne:
		if count != 3 {
			return nil
		}
		return []image.Rectangle{
			image.Rect(rect.Min.X, rect.Min.Y, rect.Min.X+halfWidth, rect.Min.Y+halfHeight),
			image.Rect(midX, rect.Min.Y, rect.Max.X, rect.Min.Y+halfHeight),
			image.Rect(rect.Min.X, midY, rect.Max.X, rect.Max.Y),
		}
	default:
		return nil
	}
}

func drawArrangedImageGallery(dc *gg.Context, rect image.Rectangle, images []image.Image, arrangement galleryArrangement) {
	if arrangement == galleryDefault {
		drawImageGallery(dc, rect, images)
		return
	}
	visible := make([]image.Image, 0, min(len(images), 4))
	for _, img := range images {
		if img == nil || img.Bounds().Empty() {
			continue
		}
		visible = append(visible, img)
		if len(visible) == 4 {
			break
		}
	}
	cells := galleryCellRects(rect, arrangement, len(visible))
	if len(cells) != len(visible) {
		drawImageGallery(dc, rect, visible)
		return
	}
	for index, img := range visible {
		drawImageContent(dc, cells[index], img)
	}
}

func naturalGalleryHeight(images []image.Image, width int, arrangement galleryArrangement) int {
	visible := make([]image.Image, 0, min(len(images), 4))
	for _, img := range images {
		if img == nil || img.Bounds().Empty() {
			continue
		}
		visible = append(visible, img)
		if len(visible) == 4 {
			break
		}
	}
	if len(visible) == 0 || width <= 16 {
		return 0
	}
	gap := min(imageGalleryGap, max(0, width-2))
	imageHeight := func(img image.Image, cellWidth int) int {
		bounds := img.Bounds()
		innerWidth := max(1, cellWidth-16)
		return int(math.Ceil(float64(innerWidth)*float64(bounds.Dy())/float64(bounds.Dx()))) + 16
	}
	if len(visible) == 1 {
		return imageHeight(visible[0], width)
	}
	cellWidth := (width - gap) / 2
	rightWidth := width - cellWidth - gap
	rowHeight := func(start, end, cellWidth int) int {
		height := 0
		for index := start; index < end; index++ {
			height = max(height, imageHeight(visible[index], cellWidth))
		}
		return height
	}
	if arrangement == galleryTwoRows && len(visible) == 2 {
		return 2*max(imageHeight(visible[0], width), imageHeight(visible[1], width)) + gap
	}
	if arrangement == galleryTwoColumns && len(visible) == 2 {
		return max(imageHeight(visible[0], cellWidth), imageHeight(visible[1], rightWidth))
	}
	if arrangement == galleryLeftOneRightTwo && len(visible) == 3 {
		rightHeight := max(imageHeight(visible[1], rightWidth), imageHeight(visible[2], rightWidth))
		return max(imageHeight(visible[0], cellWidth), 2*rightHeight+gap)
	}
	if arrangement == galleryLeftTwoRightOne && len(visible) == 3 {
		leftHeight := max(imageHeight(visible[0], cellWidth), imageHeight(visible[1], cellWidth))
		return max(2*leftHeight+gap, imageHeight(visible[2], rightWidth))
	}
	if arrangement == galleryTopOneBottomTwo && len(visible) == 3 {
		return 2*max(imageHeight(visible[0], width), rowHeight(1, 3, cellWidth)) + gap
	}
	if arrangement == galleryTopTwoBottomOne && len(visible) == 3 {
		return 2*max(rowHeight(0, 2, cellWidth), imageHeight(visible[2], width)) + gap
	}
	if len(visible) == 4 {
		return 2*max(rowHeight(0, 2, cellWidth), rowHeight(2, 4, cellWidth)) + gap
	}
	switch len(visible) {
	case 2:
		return max(imageHeight(visible[0], cellWidth), imageHeight(visible[1], rightWidth))
	case 3:
		rightCellHeight := max(imageHeight(visible[1], rightWidth), imageHeight(visible[2], rightWidth))
		return max(imageHeight(visible[0], cellWidth), 2*rightCellHeight+gap)
	default:
		rowHeight := 0
		for _, img := range visible {
			rowHeight = max(rowHeight, imageHeight(img, cellWidth))
		}
		return 2*rowHeight + gap
	}
}

func roundImageCorners(img *image.RGBA, radius int) {
	if img == nil || img.Bounds().Empty() || radius <= 0 {
		return
	}

	bounds := img.Bounds()
	radius = min(radius, bounds.Dx()/2, bounds.Dy()/2)
	if radius == 0 {
		return
	}

	applyCoverage := func(x, y int, centerX, centerY float64) {
		distance := math.Hypot(float64(x)+0.5-centerX, float64(y)+0.5-centerY)
		coverage := math.Max(0, math.Min(1, float64(radius)+0.5-distance))
		if coverage >= 1 {
			return
		}
		offset := img.PixOffset(bounds.Min.X+x, bounds.Min.Y+y)
		for channel := 0; channel < 4; channel++ {
			img.Pix[offset+channel] = uint8(math.Round(float64(img.Pix[offset+channel]) * coverage))
		}
	}

	width, height := bounds.Dx(), bounds.Dy()
	center := float64(radius)
	farCenterX, farCenterY := float64(width-radius), float64(height-radius)
	for y := 0; y < radius; y++ {
		for x := 0; x < radius; x++ {
			applyCoverage(x, y, center, center)
			applyCoverage(width-1-x, y, farCenterX, center)
			applyCoverage(x, height-1-y, center, farCenterY)
			applyCoverage(width-1-x, height-1-y, farCenterX, farCenterY)
		}
	}
}

func drawLinkCard(dc *gg.Context, fonts *fontSource, palette renderPalette, card *linkCardData, cardImage image.Image, imageIsMain bool, rect image.Rectangle) error {
	if card == nil || rect.Empty() {
		return nil
	}
	dc.SetHexColor(palette.Panel)
	dc.DrawRoundedRectangle(float64(rect.Min.X), float64(rect.Min.Y), float64(rect.Dx()), float64(rect.Dy()), 18)
	dc.Fill()
	dc.SetHexColor(palette.PanelBorder)
	dc.SetLineWidth(2)
	dc.DrawRoundedRectangle(float64(rect.Min.X), float64(rect.Min.Y), float64(rect.Dx()), float64(rect.Dy()), 18)
	dc.Stroke()

	contentX := rect.Min.X + 28
	contentWidth := float64(rect.Dx() - 56)
	if cardImage != nil && !imageIsMain {
		thumbSide := min(232, rect.Dy()-28)
		thumb := image.Rect(rect.Min.X+14, rect.Min.Y+14, rect.Min.X+14+thumbSide, rect.Max.Y-14)
		drawImageContent(dc, thumb, cardImage)
		contentX = thumb.Max.X + 24
		contentWidth = float64(rect.Max.X - contentX - 24)
	}

	title := strings.TrimSpace(card.Title)
	if title == "" {
		title = strings.TrimSpace(card.URL)
	}
	if err := drawSingleLine(dc, fonts, title, float64(contentX), float64(rect.Min.Y+48), contentWidth, 30, 20, palette.PrimaryText); err != nil {
		return err
	}
	if err := drawSingleLine(dc, fonts, card.SiteName, float64(contentX), float64(rect.Min.Y+82), contentWidth, 22, 17, palette.SecondaryText); err != nil {
		return err
	}
	if card.Description != "" {
		descriptionTop := rect.Min.Y + 96
		descriptionHeight := max(40, rect.Max.Y-descriptionTop-16)
		if err := drawBody(dc, fonts, palette, card.Description, contentX, descriptionTop, contentWidth, float64(descriptionHeight), 20, 16); err != nil {
			return err
		}
	}
	return nil
}

func drawIDBlock(dc *gg.Context, fonts *fontSource, palette renderPalette, post postData, avatar image.Image, x, y int, width float64) error {
	avatarSize := 112
	if avatar != nil {
		dc.DrawImage(makeCircleAvatar(avatar, avatarSize), x, y)
	} else {
		dc.SetHexColor(palette.Placeholder)
		dc.DrawCircle(float64(x+avatarSize/2), float64(y+avatarSize/2), float64(avatarSize/2))
		dc.Fill()
		face, err := fonts.face(42)
		if err != nil {
			return err
		}
		dc.SetFontFace(face)
		dc.SetHexColor(palette.PlaceholderText)
		initials := userInitials(post.UserName)
		w, h := dc.MeasureString(initials)
		dc.DrawString(initials, float64(x+avatarSize/2)-w/2, float64(y+avatarSize/2)+(h/3))
		face.Close()
	}

	textX := x + avatarSize + 28
	textWidth := width - float64(avatarSize+28)
	name := strings.TrimSpace(post.UserName)
	if name == "" {
		name = "Unknown user"
	}
	if err := drawSingleLine(dc, fonts, name, float64(textX), float64(y+42), textWidth, 42, 26, palette.PrimaryText); err != nil {
		return err
	}
	handle := strings.TrimPrefix(strings.TrimSpace(post.Handle), "@")
	if handle != "" {
		handle = "@" + handle
	}
	if err := drawSingleLine(dc, fonts, handle, float64(textX), float64(y+80), textWidth, 29, 22, palette.SecondaryText); err != nil {
		return err
	}
	return drawSingleLine(dc, fonts, formatPostDate(post.CreatedAt), float64(textX), float64(y+114), textWidth, 25, 19, palette.SecondaryText)
}

func drawSingleLine(dc *gg.Context, fonts *fontSource, text string, x, baseline, maxWidth float64, maxSize, minSize int, hexColor string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	for size := maxSize; size >= minSize; size -= 2 {
		face, err := fonts.face(float64(size))
		if err != nil {
			return err
		}
		dc.SetFontFace(face)
		if width, _ := dc.MeasureString(text); width <= maxWidth {
			dc.SetHexColor(hexColor)
			dc.DrawString(text, x, baseline)
			face.Close()
			return nil
		}
		face.Close()
	}

	face, err := fonts.face(float64(minSize))
	if err != nil {
		return err
	}
	defer face.Close()
	dc.SetFontFace(face)
	dc.SetHexColor(hexColor)
	runes := []rune(text)
	for len(runes) > 1 {
		runes = runes[:len(runes)-1]
		text = strings.TrimRight(string(runes), " ") + "…"
		if width, _ := dc.MeasureString(text); width <= maxWidth {
			dc.DrawString(text, x, baseline)
			return nil
		}
	}
	dc.DrawString("…", x, baseline)
	return nil
}

func drawBody(dc *gg.Context, fonts *fontSource, palette renderPalette, text string, x, y int, width, height float64, maxSize, minSize int) error {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if text == "" {
		text = "（本文なし）"
	}
	for size := maxSize; size >= minSize; size -= 2 {
		face, err := fonts.face(float64(size))
		if err != nil {
			return err
		}
		dc.SetFontFace(face)
		lines := wrapText(dc, text, width)
		lineHeight := float64(size) * 1.35
		if float64(len(lines))*lineHeight <= height {
			dc.SetHexColor(palette.PrimaryText)
			baseline := float64(face.Metrics().Ascent.Round())
			for i, line := range lines {
				dc.DrawString(line, float64(x), float64(y)+baseline+float64(i)*lineHeight)
			}
			face.Close()
			return nil
		}
		face.Close()
	}
	return errors.New("post text is too long to fit this ImagePad layout")
}

func prepareTweetContent(text string, links []postLink, cardSourceURL, mediaSourceURL string) (string, []string) {
	spans := make([]tweetURLSpan, 0, len(links)*2+4)
	for index := range links {
		link := links[index]
		aliases := uniqueNonEmpty(link.URL, link.ShortURL, link.DisplayURL)
		display := strings.TrimSpace(link.DisplayURL)
		if display == "" {
			display = strings.TrimSpace(link.URL)
		}
		for _, alias := range aliases {
			for start := 0; start < len(text); {
				offset := strings.Index(text[start:], alias)
				if offset < 0 {
					break
				}
				start += offset
				end := start + len(alias)
				if hasTweetURLBoundaries(text, start, end) {
					linkCopy := link
					spans = append(spans, tweetURLSpan{start: start, end: end, display: display, link: &linkCopy})
				}
				start = end
			}
		}
	}
	for _, match := range tweetURLPattern.FindAllStringIndex(text, -1) {
		start, end := match[0], trimTweetURLEnd(text, match[0], match[1])
		if end > start && hasTweetURLBoundaries(text, start, end) {
			spans = append(spans, tweetURLSpan{start: start, end: end, display: text[start:end]})
		}
	}
	sort.SliceStable(spans, func(i, j int) bool {
		if spans[i].start != spans[j].start {
			return spans[i].start < spans[j].start
		}
		if spans[i].end != spans[j].end {
			return spans[i].end > spans[j].end
		}
		return spans[i].link != nil && spans[j].link == nil
	})

	var cleaned strings.Builder
	visibleLinks := make([]string, 0, len(spans))
	seenLinks := make(map[string]struct{})
	position := 0
	for _, span := range spans {
		if span.start < position {
			continue
		}
		cleaned.WriteString(text[position:span.start])
		position = span.end
		if isOmittedTweetURL(span.display, span.link, cardSourceURL, mediaSourceURL) {
			continue
		}
		display := strings.TrimSpace(span.display)
		if span.link != nil {
			display = strings.TrimSpace(span.link.DisplayURL)
			if display == "" {
				display = strings.TrimSpace(span.link.URL)
			}
		}
		if display == "" {
			continue
		}
		key := strings.ToLower(display)
		if _, ok := seenLinks[key]; !ok {
			visibleLinks = append(visibleLinks, display)
			seenLinks[key] = struct{}{}
		}
	}
	cleaned.WriteString(text[position:])

	paragraphs := strings.Split(strings.ReplaceAll(cleaned.String(), "\r\n", "\n"), "\n")
	for index, paragraph := range paragraphs {
		paragraph = tweetSpacePattern.ReplaceAllString(paragraph, " ")
		paragraph = tweetSpaceBeforePunctuationPattern.ReplaceAllString(paragraph, "$1")
		paragraphs[index] = strings.TrimSpace(paragraph)
	}
	return strings.TrimSpace(strings.Join(paragraphs, "\n")), visibleLinks
}

func uniqueNonEmpty(values ...string) []string {
	unique := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	return unique
}

func hasTweetURLBoundaries(text string, start, end int) bool {
	if start > 0 {
		previous, _ := utf8.DecodeLastRuneInString(text[:start])
		if isTweetURLContinuation(previous) {
			return false
		}
	}
	if end < len(text) {
		next, _ := utf8.DecodeRuneInString(text[end:])
		if isTweetURLContinuation(next) {
			return false
		}
	}
	return true
}

func isTweetURLContinuation(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("_-./?&#=%", r)
}

func trimTweetURLEnd(text string, start, end int) int {
	for end > start {
		r, size := utf8.DecodeLastRuneInString(text[start:end])
		if strings.ContainsRune(",.;:!?…、。，！？", r) {
			end -= size
			continue
		}
		break
	}
	return end
}

func isOmittedTweetURL(rawURL string, link *postLink, cardSourceURL, mediaSourceURL string) bool {
	if isXMediaURL(rawURL) || sameURLResource(rawURL, cardSourceURL) || sameURLResource(rawURL, mediaSourceURL) {
		return true
	}
	if link == nil {
		return false
	}
	for _, alias := range uniqueNonEmpty(link.URL, link.ShortURL, link.DisplayURL) {
		if isXMediaURL(alias) || sameURLResource(alias, cardSourceURL) || sameURLResource(alias, mediaSourceURL) {
			return true
		}
	}
	return false
}

func isXMediaURL(rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	if u.Scheme == "" {
		u, err = url.Parse("https://" + strings.TrimPrefix(rawURL, "//"))
		if err != nil {
			return false
		}
	}
	host := strings.ToLower(u.Hostname())
	if host == "pic.x.com" || host == "pic.twitter.com" {
		return true
	}
	isXHost := host == "x.com" || host == "www.x.com" || host == "twitter.com" || host == "www.twitter.com"
	return isXHost && strings.Contains(strings.ToLower(u.Path), "/status/") && tweetStatusMediaPattern.MatchString(u.Path)
}

func sameURLResource(first, second string) bool {
	if strings.TrimSpace(first) == "" || strings.TrimSpace(second) == "" {
		return false
	}
	identity := func(raw string) string {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil {
			return ""
		}
		if u.Scheme == "" {
			u, err = url.Parse("https://" + strings.TrimPrefix(raw, "//"))
			if err != nil {
				return ""
			}
		}
		return strings.ToLower(u.Hostname()) + strings.TrimSuffix(u.EscapedPath(), "/")
	}
	firstID, secondID := identity(first), identity(second)
	return firstID != "" && firstID == secondID
}

type tweetBodyLine struct {
	text string
	link bool
}

func layoutTweetBody(dc *gg.Context, fonts *fontSource, text string, links []postLink, cardSourceURL, mediaSourceURL string, width, height float64, maxSize, minSize int) ([]tweetBodyLine, int, float64, error) {
	text, urls := prepareTweetContent(text, links, cardSourceURL, mediaSourceURL)
	if text == "" && len(urls) == 0 {
		text = "（本文なし）"
	}
	for size := maxSize; size >= minSize; size -= 2 {
		face, err := fonts.face(float64(size))
		if err != nil {
			return nil, 0, 0, err
		}
		dc.SetFontFace(face)
		lines := make([]tweetBodyLine, 0, 16)
		if text != "" {
			for _, line := range wrapText(dc, text, width) {
				lines = append(lines, tweetBodyLine{text: line})
			}
		}
		if len(urls) > 0 && len(lines) > 0 {
			lines = append(lines, tweetBodyLine{})
		}
		for _, url := range urls {
			for _, line := range wrapText(dc, url, width) {
				lines = append(lines, tweetBodyLine{text: line, link: true})
			}
		}
		lineHeight := float64(size) * 1.35
		if float64(len(lines))*lineHeight <= height {
			face.Close()
			return lines, size, lineHeight, nil
		}
		face.Close()
	}
	return nil, 0, 0, errors.New("post text is too long to fit this ImagePad layout")
}

func measureTweetBodyHeight(dc *gg.Context, fonts *fontSource, text string, links []postLink, cardSourceURL, mediaSourceURL string, width, height float64, maxSize, minSize int) (float64, error) {
	lines, _, lineHeight, err := layoutTweetBody(dc, fonts, text, links, cardSourceURL, mediaSourceURL, width, height, maxSize, minSize)
	if err != nil {
		return 0, err
	}
	return float64(len(lines)) * lineHeight, nil
}

func drawPostContent(dc *gg.Context, fonts *fontSource, palette renderPalette, text string, links []postLink, cardSourceURL, mediaSourceURL string, x, y int, width, height float64, maxSize, minSize int) error {
	lines, size, lineHeight, err := layoutTweetBody(dc, fonts, text, links, cardSourceURL, mediaSourceURL, width, height, maxSize, minSize)
	if err != nil {
		return err
	}
	face, err := fonts.face(float64(size))
	if err != nil {
		return err
	}
	defer face.Close()
	dc.SetFontFace(face)
	baseline := float64(face.Metrics().Ascent.Round())
	for index, line := range lines {
		color := palette.PrimaryText
		if line.link {
			color = palette.Link
		}
		dc.SetHexColor(color)
		dc.DrawString(line.text, float64(x), float64(y)+baseline+float64(index)*lineHeight)
	}
	return nil
}

func wrapText(dc *gg.Context, text string, maxWidth float64) []string {
	lines := make([]string, 0, 8)
	for _, paragraph := range strings.Split(text, "\n") {
		var line strings.Builder
		for _, r := range paragraph {
			if r == '\t' {
				r = ' '
			}
			candidate := line.String() + string(r)
			if line.Len() > 0 && r != ' ' {
				if width, _ := dc.MeasureString(candidate); width > maxWidth {
					lines = append(lines, strings.TrimRight(line.String(), " "))
					line.Reset()
				}
			}
			if line.Len() == 0 && r == ' ' {
				continue
			}
			line.WriteRune(r)
		}
		lines = append(lines, strings.TrimRight(line.String(), " "))
	}
	return lines
}

func makeCircleAvatar(src image.Image, size int) *image.RGBA {
	bounds := src.Bounds()
	side := min(bounds.Dx(), bounds.Dy())
	crop := image.Rect(
		bounds.Min.X+(bounds.Dx()-side)/2,
		bounds.Min.Y+(bounds.Dy()-side)/2,
		bounds.Min.X+(bounds.Dx()-side)/2+side,
		bounds.Min.Y+(bounds.Dy()-side)/2+side,
	)
	avatar := image.NewRGBA(image.Rect(0, 0, size, size))
	xdraw.CatmullRom.Scale(avatar, avatar.Bounds(), src, crop, draw.Src, nil)
	radius := float64(size) / 2
	radiusSquared := radius * radius
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			dx := float64(px) + 0.5 - radius
			dy := float64(py) + 0.5 - radius
			if dx*dx+dy*dy > radiusSquared {
				avatar.SetRGBA(px, py, color.RGBA{})
			}
		}
	}
	return avatar
}

func userInitials(name string) string {
	parts := strings.Fields(name)
	if len(parts) == 0 {
		return "?"
	}
	initials := []rune(parts[0])[:1]
	if len(parts) > 1 {
		initials = append(initials, []rune(parts[len(parts)-1])[:1]...)
	}
	return string(initials)
}

func formatPostDate(raw string) string {
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		parsed, err = time.Parse(time.RFC3339Nano, raw)
	}
	if err != nil {
		return raw
	}
	location, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		location = time.FixedZone("JST", 9*60*60)
	}
	return parsed.In(location).Format("2006-01-02 15:04 JST")
}

func (fs *fontSource) face(size float64) (font.Face, error) {
	return opentype.NewFace(fs.font, &opentype.FaceOptions{
		Size:    size,
		DPI:     72,
		Hinting: font.HintingFull,
	})
}
