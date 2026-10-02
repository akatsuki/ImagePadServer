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

	frameRect := image.Rect(32, 32, layout.width-32, layout.height-32)
	switch layout.kind {
	case layoutTextOnly:
		if post.Quoted != nil {
			if err := drawHeader(dc, fonts, palette, post, avatar, 112, 190, float64(layout.width-224)); err != nil {
				return nil, err
			}
			if err := drawTweetBody(dc, fonts, palette, post.Text, post.Links, cardSourceURL, post.MediaSourceURL, 112, 370, float64(layout.width-224), 340, 58, 30); err != nil {
				return nil, err
			}
			if err := drawQuotedPost(dc, fonts, palette, post.Quoted, quotedAvatar, image.Rect(112, 770, layout.width-112, 1408)); err != nil {
				return nil, err
			}
		} else if post.Card != nil {
			if err := drawHeader(dc, fonts, palette, post, avatar, 112, 330, float64(layout.width-224)); err != nil {
				return nil, err
			}
			if err := drawTweetBody(dc, fonts, palette, post.Text, post.Links, cardSourceURL, post.MediaSourceURL, 112, 510, float64(layout.width-224), 450, 58, 30); err != nil {
				return nil, err
			}
			if err := drawLinkCard(dc, fonts, palette, post.Card, cardImage, post.CardImageIsMain, image.Rect(112, 1000, layout.width-112, 1408)); err != nil {
				return nil, err
			}
		} else {
			if err := drawHeader(dc, fonts, palette, post, avatar, 112, 500, float64(layout.width-224)); err != nil {
				return nil, err
			}
			if err := drawTweetBody(dc, fonts, palette, post.Text, post.Links, cardSourceURL, post.MediaSourceURL, 112, 680, float64(layout.width-224), 650, 58, 30); err != nil {
				return nil, err
			}
		}
	case layoutStacked:
		dividerY := 760
		imageTop := 800
		galleryBottom := layout.height - 64
		if post.Quoted != nil {
			if err := drawHeader(dc, fonts, palette, post, avatar, 112, 98, float64(layout.width-224)); err != nil {
				return nil, err
			}
			if err := drawTweetBody(dc, fonts, palette, post.Text, post.Links, cardSourceURL, post.MediaSourceURL, 112, 270, float64(layout.width-224), 130, 42, 20); err != nil {
				return nil, err
			}
			if err := drawQuotedPost(dc, fonts, palette, post.Quoted, quotedAvatar, image.Rect(112, 410, layout.width-112, 774)); err != nil {
				return nil, err
			}
			dividerY = 810
			imageTop = 850
		} else if post.Card != nil {
			if err := drawHeader(dc, fonts, palette, post, avatar, 112, 98, float64(layout.width-224)); err != nil {
				return nil, err
			}
			if err := drawTweetBody(dc, fonts, palette, post.Text, post.Links, cardSourceURL, post.MediaSourceURL, 112, 270, float64(layout.width-224), 250, 48, 28); err != nil {
				return nil, err
			}
			if err := drawLinkCard(dc, fonts, palette, post.Card, cardImage, post.CardImageIsMain, image.Rect(112, 542, layout.width-112, 724)); err != nil {
				return nil, err
			}
		} else {
			const headerToBody = 172
			const bodyHeight = 420
			const dividerGap = 70
			const mediaGap = 40
			headerY := 98
			bodyY := headerY + headerToBody
			bodyWidth := float64(layout.width - 224)
			usedHeight, err := measureTweetBodyHeight(dc, fonts, post.Text, post.Links, cardSourceURL, post.MediaSourceURL, bodyWidth, bodyHeight, 48, 18)
			if err != nil {
				return nil, err
			}
			mediaHeight := naturalGalleryHeight(mediaImages, layout.width-128, layout.gallery)
			usedBodyHeight := int(math.Ceil(usedHeight))
			groupHeight := headerToBody + usedBodyHeight + dividerGap + mediaGap + mediaHeight
			availableHeight := layout.height - 128
			if usedBodyHeight <= 220 && mediaHeight > 0 && groupHeight <= availableHeight {
				headerY = 64 + (availableHeight-groupHeight)/2
				bodyY = headerY + headerToBody
				dividerY = bodyY + usedBodyHeight + dividerGap
				imageTop = dividerY + mediaGap
				galleryBottom = imageTop + mediaHeight
				frameRect = image.Rect(32, headerY-32, layout.width-32, galleryBottom+32)
			}
			if err := drawHeader(dc, fonts, palette, post, avatar, 112, headerY, float64(layout.width-224)); err != nil {
				return nil, err
			}
			if err := drawTweetBody(dc, fonts, palette, post.Text, post.Links, cardSourceURL, post.MediaSourceURL, 112, bodyY, bodyWidth, bodyHeight, 48, 18); err != nil {
				return nil, err
			}
		}
		drawDivider(dc, 96, float64(dividerY), float64(layout.width-96), float64(dividerY), palette.Divider)
		drawArrangedImageGallery(dc, image.Rect(64, imageTop, layout.width-64, galleryBottom), mediaImages, layout.gallery)
		if !frameRect.Empty() {
			drawPostFrame(dc, frameRect, palette.PanelBorder)
		}
	case layoutSideBySide:
		left := image.Rect(64, 64, 864, layout.height-64)
		drawArrangedImageGallery(dc, left, mediaImages, layout.gallery)
		drawDivider(dc, 904, 96, 904, float64(layout.height-96), palette.Divider)
		if err := drawHeader(dc, fonts, palette, post, avatar, 968, 104, float64(layout.width-1064)); err != nil {
			return nil, err
		}
		if post.Quoted != nil {
			if err := drawTweetBody(dc, fonts, palette, post.Text, post.Links, cardSourceURL, post.MediaSourceURL, 968, 286, float64(layout.width-1064), 470, 48, 26); err != nil {
				return nil, err
			}
			if err := drawQuotedPost(dc, fonts, palette, post.Quoted, quotedAvatar, image.Rect(968, 800, layout.width-64, layout.height-96)); err != nil {
				return nil, err
			}
		} else if post.Card != nil {
			if err := drawTweetBody(dc, fonts, palette, post.Text, post.Links, cardSourceURL, post.MediaSourceURL, 968, 286, float64(layout.width-1064), 770, 48, 28); err != nil {
				return nil, err
			}
			if err := drawLinkCard(dc, fonts, palette, post.Card, cardImage, post.CardImageIsMain, image.Rect(968, 1090, layout.width-64, layout.height-96)); err != nil {
				return nil, err
			}
		} else if err := drawTweetBody(dc, fonts, palette, post.Text, post.Links, cardSourceURL, post.MediaSourceURL, 968, 286, float64(layout.width-1064), 1080, 48, 28); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported layout %q", layout.kind)
	}

	rgba, ok := dc.Image().(*image.RGBA)
	if !ok {
		return nil, errors.New("renderer did not produce an RGBA image")
	}
	return rgba, nil
}

func drawQuotedPost(dc *gg.Context, fonts *fontSource, palette renderPalette, quoted *quotedPostData, avatar image.Image, rect image.Rectangle) error {
	if quoted == nil || rect.Empty() {
		return nil
	}
	dc.SetHexColor(palette.Panel)
	dc.DrawRoundedRectangle(float64(rect.Min.X), float64(rect.Min.Y), float64(rect.Dx()), float64(rect.Dy()), 18)
	dc.Fill()
	dc.SetHexColor(palette.PanelBorder)
	dc.SetLineWidth(2)
	dc.DrawRoundedRectangle(float64(rect.Min.X), float64(rect.Min.Y), float64(rect.Dx()), float64(rect.Dy()), 18)
	dc.Stroke()

	bodyX := rect.Min.X + 24
	bodyWidth := float64(rect.Dx() - 48)
	textRight := rect.Max.X - 24
	if len(quoted.MediaImages) > 0 {
		thumbnailWidth := min(240, rect.Dx()/3)
		if len(quoted.MediaImages) > 1 {
			thumbnailWidth = min(480, rect.Dx()/2)
		}
		thumbnail := image.Rect(rect.Max.X-24-thumbnailWidth, rect.Min.Y+42, rect.Max.X-24, rect.Max.Y-24)
		drawImageGallery(dc, thumbnail, quoted.MediaImages)
		textRight = thumbnail.Min.X - 24
		bodyWidth = float64(textRight - bodyX)
	}

	label := "引用ポスト"
	if quoted.Relation == "retweet" {
		label = "リポスト"
	}
	if err := drawSingleLine(dc, fonts, label, float64(rect.Min.X+24), float64(rect.Min.Y+28), float64(rect.Dx()-48), 18, 16, palette.SecondaryText); err != nil {
		return err
	}

	avatarSize := 64
	avatarX := rect.Min.X + 24
	avatarY := rect.Min.Y + 42
	if avatar != nil {
		dc.DrawImage(makeCircleAvatar(avatar, avatarSize), avatarX, avatarY)
	} else {
		dc.SetHexColor(palette.Placeholder)
		dc.DrawCircle(float64(avatarX+avatarSize/2), float64(avatarY+avatarSize/2), float64(avatarSize/2))
		dc.Fill()
	}
	textX := avatarX + avatarSize + 18
	textWidth := float64(textRight - textX)
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
	bodyTop := avatarY + avatarSize + 18
	bodyHeight := float64(rect.Max.Y - bodyTop - 20)
	if bodyHeight <= 0 {
		return nil
	}
	mediaSourceURL := ""
	if quoted.Photo != nil {
		mediaSourceURL = quoted.Photo.URL
	}
	return drawTweetBody(dc, fonts, palette, quoted.Text, quoted.Links, "", mediaSourceURL, bodyX, bodyTop, bodyWidth, bodyHeight, 28, 16)
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

func drawPostFrame(dc *gg.Context, rect image.Rectangle, hexColor string) {
	if rect.Empty() {
		return
	}
	dc.SetHexColor(hexColor)
	dc.SetLineWidth(3)
	dc.DrawRoundedRectangle(float64(rect.Min.X), float64(rect.Min.Y), float64(rect.Dx()), float64(rect.Dy()), 28)
	dc.Stroke()
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
	if len(photos) < 2 || len(photos) > 3 {
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
		candidates = append(candidates,
			candidate{layout: canvasLayout{kind: layoutStacked, width: 1536, height: 2048, gallery: galleryLeftOneRightTwo}, mediaBounds: image.Rect(0, 0, 1408, 1184)},
			candidate{layout: canvasLayout{kind: layoutStacked, width: 1536, height: 2048, gallery: galleryLeftTwoRightOne}, mediaBounds: image.Rect(0, 0, 1408, 1184)},
			candidate{layout: canvasLayout{kind: layoutSideBySide, width: 2048, height: 1536, gallery: galleryTopOneBottomTwo}, mediaBounds: image.Rect(0, 0, 800, 1408)},
			candidate{layout: canvasLayout{kind: layoutSideBySide, width: 2048, height: 1536, gallery: galleryTopTwoBottomOne}, mediaBounds: image.Rect(0, 0, 800, 1408)},
		)
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
	if rect.Empty() || (count != 2 && count != 3) {
		return nil
	}
	gap := min(imageGalleryGap, max(0, rect.Dx()-2), max(0, rect.Dy()-2))
	halfWidth := (rect.Dx() - gap) / 2
	halfHeight := (rect.Dy() - gap) / 2
	midX := rect.Min.X + halfWidth + gap
	midY := rect.Min.Y + halfHeight + gap
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
	if arrangement == galleryTwoColumns && len(visible) == 2 {
		return max(imageHeight(visible[0], cellWidth), imageHeight(visible[1], rightWidth))
	}
	if arrangement == galleryLeftOneRightTwo && len(visible) == 3 {
		rightCellHeight := max(imageHeight(visible[1], rightWidth), imageHeight(visible[2], rightWidth))
		return max(imageHeight(visible[0], cellWidth), 2*rightCellHeight+gap)
	}
	if arrangement == galleryLeftTwoRightOne && len(visible) == 3 {
		leftCellHeight := max(imageHeight(visible[0], cellWidth), imageHeight(visible[1], cellWidth))
		return max(2*leftCellHeight+gap, imageHeight(visible[2], rightWidth))
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

func drawHeader(dc *gg.Context, fonts *fontSource, palette renderPalette, post postData, avatar image.Image, x, y int, width float64) error {
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

func drawTweetBody(dc *gg.Context, fonts *fontSource, palette renderPalette, text string, links []postLink, cardSourceURL, mediaSourceURL string, x, y int, width, height float64, maxSize, minSize int) error {
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
