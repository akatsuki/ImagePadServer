package xpostimage

import (
	"errors"
	"image"
	"path/filepath"
	"runtime"
	"testing"
)

func TestArrangeCenteredColumnUsesUniformGapsAndCentersBounds(t *testing.T) {
	canvas := image.Pt(400, 800)
	sizes := []image.Point{image.Pt(300, 100), image.Pt(200, 4), image.Pt(300, 300)}
	rects, bounds := arrangeCenteredColumn(canvas, sizes, 20)
	want := []image.Rectangle{
		image.Rect(50, 178, 350, 278),
		image.Rect(100, 298, 300, 302),
		image.Rect(50, 322, 350, 622),
	}
	if len(rects) != len(want) {
		t.Fatalf("got %d blocks, want %d", len(rects), len(want))
	}
	for index := range want {
		if rects[index] != want[index] {
			t.Errorf("block %d = %v, want %v", index, rects[index], want[index])
		}
	}
	if bounds != image.Rect(50, 178, 350, 622) {
		t.Fatalf("group bounds = %v, want %v", bounds, image.Rect(50, 178, 350, 622))
	}
}

func TestArrangeCenteredRowCentersEachBlockAgainstWholeGroup(t *testing.T) {
	canvas := image.Pt(800, 400)
	sizes := []image.Point{image.Pt(250, 300), image.Pt(4, 300), image.Pt(300, 100)}
	rects, bounds := arrangeCenteredRow(canvas, sizes, 20)
	want := []image.Rectangle{
		image.Rect(103, 50, 353, 350),
		image.Rect(373, 50, 377, 350),
		image.Rect(397, 150, 697, 250),
	}
	if len(rects) != len(want) {
		t.Fatalf("got %d blocks, want %d", len(rects), len(want))
	}
	for index := range want {
		if rects[index] != want[index] {
			t.Errorf("block %d = %v, want %v", index, rects[index], want[index])
		}
	}
	if bounds != image.Rect(103, 50, 697, 350) {
		t.Fatalf("group bounds = %v, want %v", bounds, image.Rect(103, 50, 697, 350))
	}
}

func TestFitStackedImageMaximizesGalleryWithoutClippingContent(t *testing.T) {
	measureBody := func(maxHeight int) (int, error) {
		if maxHeight < 160 {
			return 0, errors.New("post content does not fit")
		}
		return 160, nil
	}
	galleryHeight, bodyHeight, err := fitStackedImageAndContent(400, 100, 350, 20, 3, 24, measureBody)
	if err != nil {
		t.Fatalf("fitStackedImageAndContent() error = %v", err)
	}
	if galleryHeight != 97 {
		t.Fatalf("gallery height = %d, want 97", galleryHeight)
	}
	if bodyHeight != 160 {
		t.Fatalf("body height = %d, want 160", bodyHeight)
	}
}

func TestFrameBoundsExpandCompositionAfterCentering(t *testing.T) {
	content := image.Rect(64, 178, 336, 622)
	if got, want := frameBounds(content, 32), image.Rect(32, 146, 368, 654); got != want {
		t.Fatalf("frame bounds = %v, want %v", got, want)
	}
}

func TestFrameBoundsAddsTopMarginAfterCentering(t *testing.T) {
	content := image.Rect(64, 178, 336, 622)
	got := frameBoundsWithInsets(content, 32, 48, 32, 32)
	if want := image.Rect(32, 130, 368, 654); got != want {
		t.Fatalf("frame bounds = %v, want %v", got, want)
	}
}

func TestQuoteBlockUsesNaturalHeightAndFullAvailableWidth(t *testing.T) {
	layout := quotedPostBlockLayoutFor(600, 88, 54, 180, 80)
	if got, want := layout.Bounds, image.Rect(0, 0, 600, 274); got != want {
		t.Fatalf("quote bounds = %v, want %v", got, want)
	}
	if got, want := layout.ID, image.Rect(28, 76, 364, 164); got != want {
		t.Fatalf("quote ID block = %v, want %v", got, want)
	}
	if got, want := layout.Content, image.Rect(28, 192, 364, 246); got != want {
		t.Fatalf("quote content = %v, want %v", got, want)
	}
	if got, want := layout.Media, image.Rect(392, 76, 572, 156); got != want {
		t.Fatalf("quote media = %v, want %v", got, want)
	}
}

func TestNaturalGalleryHeightFollowsSelectedArrangement(t *testing.T) {
	images := []image.Image{
		image.NewRGBA(image.Rect(0, 0, 100, 100)),
		image.NewRGBA(image.Rect(0, 0, 100, 100)),
		image.NewRGBA(image.Rect(0, 0, 100, 100)),
	}
	cases := []struct {
		name        string
		arrangement galleryArrangement
		wantHeight  int
	}{
		{name: "left one right two", arrangement: galleryLeftOneRightTwo, wantHeight: 220},
		{name: "left two right one", arrangement: galleryLeftTwoRightOne, wantHeight: 220},
		{name: "top one bottom two", arrangement: galleryTopOneBottomTwo, wantHeight: 452},
		{name: "top two bottom one", arrangement: galleryTopTwoBottomOne, wantHeight: 452},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := naturalGalleryHeight(images, 220, tc.arrangement); got != tc.wantHeight {
				t.Fatalf("gallery height = %d, want %d", got, tc.wantHeight)
			}
		})
	}
}

func TestNaturalGalleryHeightFitsFourImagesInEqualGridCells(t *testing.T) {
	images := []image.Image{
		image.NewRGBA(image.Rect(0, 0, 100, 100)),
		image.NewRGBA(image.Rect(0, 0, 100, 100)),
		image.NewRGBA(image.Rect(0, 0, 100, 200)),
		image.NewRGBA(image.Rect(0, 0, 100, 200)),
	}
	if got, want := naturalGalleryHeight(images, 220, galleryDefault), 396; got != want {
		t.Fatalf("gallery height = %d, want %d", got, want)
	}
}

func TestNaturalGalleryHeightFitsTwoRowsForTwoImages(t *testing.T) {
	images := []image.Image{
		image.NewRGBA(image.Rect(0, 0, 100, 100)),
		image.NewRGBA(image.Rect(0, 0, 100, 100)),
	}
	if got, want := naturalGalleryHeight(images, 220, galleryTwoRows), 452; got != want {
		t.Fatalf("gallery height = %d, want %d", got, want)
	}
}

func TestChooseMediaLayoutForFourImagesUsesGalleryArea(t *testing.T) {
	photos := []photoInfo{
		{Width: 1200, Height: 600},
		{Width: 600, Height: 1200},
		{Width: 600, Height: 1200},
		{Width: 600, Height: 1200},
	}
	layout := chooseMediaLayout(&photos[0], photos)
	if layout.kind != layoutSideBySide || layout.width != 2048 || layout.height != 1536 {
		t.Fatalf("layout = %+v, want landscape side-by-side canvas from the best four-image gallery area", layout)
	}
}

func TestRenderPostDrawsShadowedFrameAroundCompositionForEveryLayout(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate test source")
	}
	fontPath := filepath.Join(filepath.Dir(sourceFile), "..", "video", "fonts", "NotoSansJP-Regular.ttf")

	cases := []struct {
		name      string
		photo     image.Image
		photoInfo *photoInfo
		wantSize  image.Point
		frameX    int
	}{
		{name: "text only landscape", wantSize: image.Pt(2048, 1536), frameX: 80},
		{
			name:      "landscape photo portrait canvas",
			photo:     image.NewRGBA(image.Rect(0, 0, 640, 480)),
			photoInfo: &photoInfo{Width: 640, Height: 480},
			wantSize:  image.Pt(1536, 2048),
			frameX:    32,
		},
		{
			name:      "portrait photo landscape canvas",
			photo:     image.NewRGBA(image.Rect(0, 0, 480, 640)),
			photoInfo: &photoInfo{Width: 480, Height: 640},
			wantSize:  image.Pt(2048, 1536),
			frameX:    32,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, theme := range []string{"light", "dark"} {
				t.Run(theme, func(t *testing.T) {
					post := postData{
						TweetID:  "1",
						Text:     "テスト投稿",
						UserName: "ImagePad",
						Handle:   "@imagepad",
						Photo:    tc.photoInfo,
					}
					if tc.photo != nil {
						post.MediaImages = []image.Image{tc.photo}
					}

					rendered, err := renderPost(post, tc.photo, nil, nil, nil, theme, fontPath)
					if err != nil {
						t.Fatalf("renderPost() error = %v", err)
					}
					if got := image.Pt(rendered.Bounds().Dx(), rendered.Bounds().Dy()); got != tc.wantSize {
						t.Fatalf("canvas size = %v, want %v", got, tc.wantSize)
					}

					border := rendered.RGBAAt(tc.frameX, tc.wantSize.Y/2)
					outside := rendered.RGBAAt(tc.frameX-16, tc.wantSize.Y/2)
					if border == outside {
						t.Fatal("post frame is missing around the content bounds")
					}
				})
			}
		})
	}
}

func TestRenderBuildsImageBlocksWithTwoThreeAndFourImages(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate test source")
	}
	fontPath := filepath.Join(filepath.Dir(sourceFile), "..", "video", "fonts", "NotoSansJP-Regular.ttf")
	cases := []struct {
		name   string
		images []image.Image
	}{
		{
			name: "two images",
			images: []image.Image{
				image.NewRGBA(image.Rect(0, 0, 480, 640)),
				image.NewRGBA(image.Rect(0, 0, 480, 640)),
			},
		},
		{
			name: "three images",
			images: []image.Image{
				image.NewRGBA(image.Rect(0, 0, 640, 480)),
				image.NewRGBA(image.Rect(0, 0, 480, 640)),
				image.NewRGBA(image.Rect(0, 0, 480, 640)),
			},
		},
		{
			name: "four mixed images",
			images: []image.Image{
				image.NewRGBA(image.Rect(0, 0, 640, 480)),
				image.NewRGBA(image.Rect(0, 0, 480, 640)),
				image.NewRGBA(image.Rect(0, 0, 480, 640)),
				image.NewRGBA(image.Rect(0, 0, 480, 640)),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			infos := photoInfoForImages(tc.images)
			layout := chooseMediaLayout(&infos[0], infos)
			post := postData{
				TweetID:     "1",
				Text:        "テスト投稿",
				UserName:    "ImagePad",
				Handle:      "@imagepad",
				Photo:       &infos[0],
				MediaImages: tc.images,
			}
			rendered, err := renderPost(post, nil, nil, nil, nil, "light", fontPath)
			if err != nil {
				t.Fatalf("renderPost() error = %v", err)
			}
			if got, want := image.Pt(rendered.Bounds().Dx(), rendered.Bounds().Dy()), image.Pt(layout.width, layout.height); got != want {
				t.Fatalf("canvas size = %v, want %v", got, want)
			}
		})
	}
}

func TestRenderQuotedPortraitCentersContentBeforeFrame(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate test source")
	}
	fontPath := filepath.Join(filepath.Dir(sourceFile), "..", "video", "fonts", "NotoSansJP-Regular.ttf")
	photo := image.NewRGBA(image.Rect(0, 0, 640, 480))
	post := postData{
		TweetID:  "1",
		Text:     "テスト投稿",
		UserName: "ImagePad",
		Handle:   "@imagepad",
		Photo:    &photoInfo{Width: 640, Height: 480},
		MediaImages: []image.Image{
			photo,
		},
		Quoted: &quotedPostData{
			Text:     "引用元の投稿",
			UserName: "Quoted user",
			Handle:   "quoted_user",
		},
	}

	rendered, err := renderPost(post, photo, nil, nil, nil, "light", fontPath)
	if err != nil {
		t.Fatalf("renderPost() error = %v", err)
	}
	background := rendered.RGBAAt(16, 32)
	if got := rendered.RGBAAt(rendered.Bounds().Dx()/2, 32); got != background {
		t.Fatal("post frame starts at the canvas edge instead of wrapping the centered content")
	}
	if got := rendered.RGBAAt(32, rendered.Bounds().Dy()/2); got == background {
		t.Fatal("post frame is missing around the centered content")
	}
}
