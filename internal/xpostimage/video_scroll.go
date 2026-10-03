package xpostimage

import (
	"fmt"
	"image"
	"image/draw"
	"math"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/fogleman/gg"
	"imagepadserver/internal/xpostmodel"
	"imagepadserver/internal/xposttts"
)

const scrollTileHeight = 1024
const maxScrollBodyBytes = 128 << 20

type fixedVideoGeometry struct {
	frame, body, quote, quoteBody image.Rectangle
	header                        image.Point
	font, quoteFont               int
	quoteScale                    float64
}

func fixedVideoLayout(width int, hasQuote bool) fixedVideoGeometry {
	w := min(width-64, 1440)
	x := (width - w) / 2
	g := fixedVideoGeometry{frame: image.Rect(x, 76, x+w, 1004), header: image.Pt(x+44, 120), font: 52, quoteScale: 1.5, quoteFont: 42}
	if width < 1200 {
		g.font, g.quoteScale, g.quoteFont = 42, 1.25, 35
	}
	g.body = image.Rect(x+44, 288, x+w-44, 960)
	if hasQuote {
		g.quote = image.Rect(g.body.Min.X, 550, g.body.Max.X, 960)
		g.body.Max.Y = g.quote.Min.Y - 32
		pad := int(math.Round(24 * g.quoteScale))
		top := int(math.Round(124 * g.quoteScale))
		bottom := int(math.Round(20 * g.quoteScale))
		g.quoteBody = image.Rect(g.quote.Min.X+pad, g.quote.Min.Y+top, g.quote.Max.X-pad, g.quote.Max.Y-bottom)
	}
	return g
}

func renderFixedVideoCards(post xpostmodel.Post, palette renderPalette, fonts *fontSource, outDir, credit string, avatars videoAvatars) ([]xpostmodel.Page, error) {
	sources := []xpostmodel.Post{post}
	if post.Quoted != nil {
		sources = append(sources, *post.Quoted)
	}
	pages := make([]xpostmodel.Page, len(sources))
	for i, source := range sources {
		spoken, err := xposttts.BuildSpeechText(source)
		if err != nil {
			return nil, fmt.Errorf("validate X post narration text: %w", err)
		}
		pages[i].SpeechText, pages[i].Quoted = spoken, i > 0
	}
	for _, width := range []int{1920, 960} {
		kind := "card"
		if width == 960 {
			kind = "panel"
		}
		g := fixedVideoLayout(width, post.Quoted != nil)
		bodies := make([]*xpostmodel.ScrollView, len(sources))
		for i, source := range sources {
			rect, size, bg := g.body, g.font, palette.Background
			if i > 0 {
				rect, size, bg = g.quoteBody, g.quoteFont, palette.Panel
			}
			view, err := makeScrollBody(source, pages[i].SpeechText, rect, size, bg, palette, fonts, outDir, fmt.Sprintf("post-%s-body-%d", kind, i))
			if err != nil {
				return nil, err
			}
			bodies[i] = view
			if width == 1920 {
				pages[i].CardScroll = view
			} else {
				pages[i].PanelScroll = view
			}
		}
		chrome, err := fixedVideoChrome(width, post, g, palette, fonts, credit, avatars)
		if err != nil {
			return nil, err
		}
		for i := range pages {
			path := filepath.Join(outDir, fmt.Sprintf("post-%s-%03d.png", kind, i+1))
			endPath := filepath.Join(outDir, fmt.Sprintf("post-%s-%03d-end.png", kind, i+1))
			start := make([]int, len(bodies))
			for previous := 0; previous < i; previous++ {
				start[previous] = max(0, bodies[previous].ContentHeight-bodies[previous].Height)
			}
			if err := saveScrollScreen(chrome, bodies, start, path); err != nil {
				return nil, err
			}
			start[i] = max(0, bodies[i].ContentHeight-bodies[i].Height)
			if err := saveScrollScreen(chrome, bodies, start, endPath); err != nil {
				return nil, err
			}
			if width == 1920 {
				pages[i].CardPath, pages[i].CardEndPath = path, endPath
			} else {
				pages[i].PanelPath, pages[i].PanelEndPath = path, endPath
			}
		}
	}
	for _, page := range pages {
		bytes := int64(page.CardScroll.Width)*int64(page.CardScroll.ContentHeight)*4 + int64(page.PanelScroll.Width)*int64(page.PanelScroll.ContentHeight)*4
		if bytes > maxScrollBodyBytes {
			return nil, fmt.Errorf("X post scrolling body exceeds the %d MiB pixel limit", maxScrollBodyBytes>>20)
		}
	}
	return pages, nil
}

func fixedVideoChrome(width int, post xpostmodel.Post, g fixedVideoGeometry, palette renderPalette, fonts *fontSource, credit string, avatars videoAvatars) (image.Image, error) {
	dc := gg.NewContext(width, 1080)
	dc.SetHexColor(palette.Background)
	dc.Clear()
	drawPostFrame(dc, g.frame, palette)
	if err := drawHeader(dc, fonts, palette, videoPostData(post, ""), avatars.post, g.header.X, g.header.Y, float64(g.body.Dx())); err != nil {
		return nil, err
	}
	if post.Quoted != nil {
		data := videoQuotedData(*post.Quoted, "")
		data.Links = nil
		if err := drawQuotedPostScaled(dc, fonts, palette, &data, avatars.quoted, g.quote, g.quoteScale); err != nil {
			return nil, err
		}
	}
	if err := drawSingleLine(dc, fonts, strings.TrimSpace(credit), float64(g.frame.Min.X), 1050, float64(g.frame.Dx()), 24, 18, palette.SecondaryText); err != nil {
		return nil, err
	}
	return dc.Image(), nil
}

type mappedBodyLine struct {
	text   string
	link   bool
	prefix int
}

// Keep source boundaries while wrapping; artificial line breaks must not enter TTS.
func wrapMappedBody(dc *gg.Context, text string, width float64) []mappedBodyLine {
	var lines []mappedBodyLine
	position := 0
	for _, paragraph := range strings.Split(text, "\n") {
		var line strings.Builder
		for _, r := range paragraph {
			if r == '\t' {
				r = ' '
			}
			candidate := line.String() + string(r)
			if line.Len() > 0 && r != ' ' {
				if w, _ := dc.MeasureString(candidate); w > width {
					lines = append(lines, mappedBodyLine{text: strings.TrimRight(line.String(), " "), prefix: position})
					line.Reset()
				}
			}
			position++
			if line.Len() == 0 && r == ' ' {
				continue
			}
			line.WriteRune(r)
		}
		lines = append(lines, mappedBodyLine{text: strings.TrimRight(line.String(), " "), prefix: position})
		position++ // original paragraph separator, except after the final line
	}
	return lines
}

func withoutSpeechSpaces(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, text)
}

func makeScrollBody(post xpostmodel.Post, speech string, rect image.Rectangle, size int, bg string, palette renderPalette, fonts *fontSource, outDir, prefix string) (*xpostmodel.ScrollView, error) {
	data := videoPostData(post, videoPostText(post))
	body, links := prepareTweetContent(data.Text, data.Links, "", "")
	projection, err := xposttts.BuildSpeechText(xpostmodel.Post{RawText: body})
	if err != nil {
		return nil, err
	}
	if withoutSpeechSpaces(projection) != withoutSpeechSpaces(speech) {
		return nil, fmt.Errorf("X scrolling body differs from narrated content")
	}
	dc := gg.NewContext(1, 1)
	face, err := fonts.face(float64(size))
	if err != nil {
		return nil, err
	}
	defer face.Close()
	dc.SetFontFace(face)
	lines := wrapMappedBody(dc, body, float64(rect.Dx()))
	if body == "" && len(links) == 0 {
		lines = []mappedBodyLine{{text: "（本文なし）"}}
	}
	if len(links) > 0 && body != "" {
		lines = append(lines, mappedBodyLine{prefix: utf8.RuneCountInString(body)})
	}
	for _, link := range links {
		for _, line := range wrapText(dc, link, float64(rect.Dx())) {
			lines = append(lines, mappedBodyLine{text: line, link: true, prefix: utf8.RuneCountInString(body)})
		}
	}
	lineHeight := float64(size) * 1.35
	contentHeight := max(1, int(math.Ceil(float64(len(lines))*lineHeight)))
	if int64(rect.Dx())*int64(contentHeight)*4 > maxScrollBodyBytes/2 {
		return nil, fmt.Errorf("X post scrolling body exceeds the %d MiB view limit", maxScrollBodyBytes>>21)
	}
	view := &xpostmodel.ScrollView{X: rect.Min.X, Y: rect.Min.Y, Width: rect.Dx(), Height: rect.Dy(), ContentHeight: contentHeight}
	// Map non-whitespace display characters onto the canonical body-only speech.
	var spokenEnds []int
	for i, r := range []rune(speech) {
		if !unicode.IsSpace(r) {
			spokenEnds = append(spokenEnds, i+1)
		}
	}
	bodyRunes := []rune(body)
	previous := 0
	for i, line := range lines {
		spokenPrefix, err := xposttts.BuildSpeechText(xpostmodel.Post{RawText: string(bodyRunes[:min(line.prefix, len(bodyRunes))])})
		if err != nil {
			return nil, err
		}
		count := utf8.RuneCountInString(withoutSpeechSpaces(spokenPrefix))
		if count > len(spokenEnds) {
			return nil, fmt.Errorf("X scrolling line exceeds narration mapping")
		}
		end := previous
		if count > 0 {
			end = spokenEnds[count-1]
		}
		view.Lines = append(view.Lines, xpostmodel.ScrollLine{StartRune: previous, EndRune: end, Top: float64(i) * lineHeight, Bottom: float64(i+1) * lineHeight})
		previous = end
	}
	if previous != utf8.RuneCountInString(speech) {
		return nil, fmt.Errorf("X scrolling body does not reach the narration end")
	}
	baseline := float64(face.Metrics().Ascent.Round())
	for top := 0; top < contentHeight; top += scrollTileHeight {
		h := min(scrollTileHeight, contentHeight-top)
		tile := gg.NewContext(rect.Dx(), h)
		tile.SetHexColor(bg)
		tile.Clear()
		tile.SetFontFace(face)
		first := max(0, int(float64(top)/lineHeight)-1)
		last := min(len(lines), int(float64(top+h)/lineHeight)+2)
		for i := first; i < last; i++ {
			textColor := palette.PrimaryText
			if lines[i].link {
				textColor = palette.Link
			}
			tile.SetHexColor(textColor)
			tile.DrawString(lines[i].text, 0, baseline+float64(i)*lineHeight-float64(top))
		}
		path := filepath.Join(outDir, fmt.Sprintf("%s-%03d.png", prefix, len(view.Tiles)))
		if err := tile.SavePNG(path); err != nil {
			return nil, err
		}
		view.Tiles = append(view.Tiles, xpostmodel.ScrollTile{Path: path, Top: top, Height: h})
	}
	return view, nil
}

func saveScrollScreen(chrome image.Image, views []*xpostmodel.ScrollView, offsets []int, path string) error {
	dc := gg.NewContextForImage(chrome)
	for i, view := range views {
		if err := paintScrollViewport(dc, view, offsets[i]); err != nil {
			return err
		}
	}
	return dc.SavePNG(path)
}

func paintScrollViewport(dc *gg.Context, view *xpostmodel.ScrollView, offset int) error {
	// Every tile has an opaque background. Extend the final tile's last pixel
	// downwards so short bodies also clear the placeholder beneath the quote.
	var background image.Image
	for _, tile := range view.Tiles {
		if tile.Top+tile.Height <= offset || tile.Top >= offset+view.Height {
			continue
		}
		f, err := os.Open(tile.Path)
		if err != nil {
			return err
		}
		img, _, err := image.Decode(f)
		f.Close()
		if err != nil {
			return err
		}
		background = img
		start, end := max(offset, tile.Top), min(offset+view.Height, tile.Top+tile.Height)
		source := image.Rect(0, start-tile.Top, view.Width, end-tile.Top)
		target := image.Rect(view.X, view.Y+start-offset, view.X+view.Width, view.Y+end-offset)
		out := dc.Image().(draw.Image)
		draw.Draw(out, target, img, source.Min, draw.Src)
	}
	if view.ContentHeight < view.Height && background != nil {
		// The tile margin is an unpainted solid pixel at its right-hand edge.
		c := background.At(background.Bounds().Max.X-1, background.Bounds().Max.Y-1)
		draw.Draw(dc.Image().(draw.Image), image.Rect(view.X, view.Y+view.ContentHeight, view.X+view.Width, view.Y+view.Height), &image.Uniform{C: c}, image.Point{}, draw.Src)
	}
	return nil
}
