package xpostimage

import (
	"github.com/fogleman/gg"
	"image"
	"math"
	"strings"
)

// Video chrome reuses the released still-image header and body rendering.
func drawHeader(dc *gg.Context, fonts *fontSource, palette renderPalette, post postData, avatar image.Image, x, y int, width float64) error {
	return drawIDBlock(dc, fonts, palette, post, avatar, x, y, width)
}

func drawTweetBody(dc *gg.Context, fonts *fontSource, palette renderPalette, text string, links []postLink, cardSourceURL, mediaSourceURL string, x, y int, width, height float64, maxSize, minSize int) error {
	return drawPostContent(dc, fonts, palette, text, links, cardSourceURL, mediaSourceURL, x, y, width, height, maxSize, minSize)
}

func drawQuotedPostScaled(dc *gg.Context, fonts *fontSource, palette renderPalette, quoted *quotedPostData, avatar image.Image, rect image.Rectangle, scale float64) error {
	if quoted == nil || rect.Empty() {
		return nil
	}
	s := func(value int) int { return int(math.Round(float64(value) * scale)) }
	dc.SetHexColor(palette.Panel)
	dc.DrawRoundedRectangle(float64(rect.Min.X), float64(rect.Min.Y), float64(rect.Dx()), float64(rect.Dy()), float64(s(18)))
	dc.Fill()
	dc.SetHexColor(palette.PanelBorder)
	dc.SetLineWidth(float64(s(2)))
	dc.DrawRoundedRectangle(float64(rect.Min.X), float64(rect.Min.Y), float64(rect.Dx()), float64(rect.Dy()), float64(s(18)))
	dc.Stroke()

	bodyX := rect.Min.X + s(24)
	bodyWidth := float64(rect.Dx() - s(48))
	textRight := rect.Max.X - s(24)
	if len(quoted.MediaImages) > 0 {
		thumbnailWidth := min(s(240), rect.Dx()/3)
		if len(quoted.MediaImages) > 1 {
			thumbnailWidth = min(s(480), rect.Dx()/2)
		}
		thumbnail := image.Rect(rect.Max.X-s(24)-thumbnailWidth, rect.Min.Y+s(42), rect.Max.X-s(24), rect.Max.Y-s(24))
		drawImageGallery(dc, thumbnail, quoted.MediaImages)
		textRight = thumbnail.Min.X - s(24)
		bodyWidth = float64(textRight - bodyX)
	}

	label := "引用ポスト"
	if quoted.Relation == "retweet" {
		label = "リポスト"
	}
	if err := drawSingleLine(dc, fonts, label, float64(rect.Min.X+s(24)), float64(rect.Min.Y+s(28)), float64(rect.Dx()-s(48)), s(18), s(16), palette.SecondaryText); err != nil {
		return err
	}

	avatarSize := s(64)
	avatarX := rect.Min.X + s(24)
	avatarY := rect.Min.Y + s(42)
	if avatar != nil {
		dc.DrawImage(makeCircleAvatar(avatar, avatarSize), avatarX, avatarY)
	} else {
		dc.SetHexColor(palette.Placeholder)
		dc.DrawCircle(float64(avatarX+avatarSize/2), float64(avatarY+avatarSize/2), float64(avatarSize/2))
		dc.Fill()
	}
	textX := avatarX + avatarSize + s(18)
	textWidth := float64(textRight - textX)
	name := strings.TrimSpace(quoted.UserName)
	if name == "" {
		name = "Unknown user"
	}
	if err := drawSingleLine(dc, fonts, name, float64(textX), float64(avatarY+s(27)), textWidth, s(25), s(17), palette.PrimaryText); err != nil {
		return err
	}
	handle := strings.TrimPrefix(strings.TrimSpace(quoted.Handle), "@")
	if handle != "" {
		handle = "@" + handle
	}
	if err := drawSingleLine(dc, fonts, handle, float64(textX), float64(avatarY+s(51)), textWidth, s(18), s(14), palette.SecondaryText); err != nil {
		return err
	}
	if err := drawSingleLine(dc, fonts, formatPostDate(quoted.CreatedAt), float64(textX), float64(avatarY+s(73)), textWidth, s(16), s(13), palette.SecondaryText); err != nil {
		return err
	}
	bodyTop := avatarY + avatarSize + s(18)
	bodyHeight := float64(rect.Max.Y - bodyTop - s(20))
	if bodyHeight <= 0 {
		return nil
	}
	mediaSourceURL := ""
	if quoted.Photo != nil {
		mediaSourceURL = quoted.Photo.URL
	}
	return drawTweetBody(dc, fonts, palette, quoted.Text, quoted.Links, "", mediaSourceURL, bodyX, bodyTop, bodyWidth, bodyHeight, s(28), s(16))
}
