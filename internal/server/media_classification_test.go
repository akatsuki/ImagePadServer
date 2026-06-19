package server

import (
	"context"
	"testing"

	"imagepadserver/internal/video"
)

func TestClassifyUploadedMedia(t *testing.T) {
	t.Run("image detected before ffprobe", func(t *testing.T) {
		probeCalled := false
		class, err := classifyUploadedMedia(context.Background(), true,
			"photo.jpg", "image/jpeg", "/tmp/photo.jpg",
			func(_ context.Context, _ string) (video.MediaProbe, error) {
				probeCalled = true
				return video.MediaProbe{}, nil
			})
		if err != nil {
			t.Fatal(err)
		}
		if class != video.MediaUnsupported {
			t.Fatalf("class = %q; want %q", class, video.MediaUnsupported)
		}
		if probeCalled {
			t.Fatal("expected probe not to be called for image")
		}
	})

	t.Run("raw detected before ffprobe", func(t *testing.T) {
		probeCalled := false
		class, err := classifyUploadedMedia(context.Background(), true,
			"photo.CR3", "image/x-canon-cr3", "/tmp/photo.CR3",
			func(_ context.Context, _ string) (video.MediaProbe, error) {
				probeCalled = true
				return video.MediaProbe{}, nil
			})
		if err != nil {
			t.Fatal(err)
		}
		if class != video.MediaUnsupported {
			t.Fatalf("class = %q; want %q", class, video.MediaUnsupported)
		}
		if probeCalled {
			t.Fatal("expected probe not to be called for raw")
		}
	})

	t.Run("extensionless audio", func(t *testing.T) {
		class, err := classifyUploadedMedia(context.Background(), true,
			"track", "audio/mpeg", "/tmp/track",
			func(_ context.Context, _ string) (video.MediaProbe, error) {
				return video.MediaProbe{Streams: []video.MediaStream{
					{Index: 0, CodecType: "audio", CodecName: "mp3"},
				}}, nil
			})
		if err != nil {
			t.Fatal(err)
		}
		if class != video.MediaAudio {
			t.Fatalf("class = %q; want %q", class, video.MediaAudio)
		}
	})

	t.Run("audio with attached cover", func(t *testing.T) {
		class, err := classifyUploadedMedia(context.Background(), true,
			"song.m4a", "audio/mp4", "/tmp/song.m4a",
			func(_ context.Context, _ string) (video.MediaProbe, error) {
				return video.MediaProbe{Streams: []video.MediaStream{
					{Index: 0, CodecType: "audio", CodecName: "aac"},
					{Index: 1, CodecType: "video", CodecName: "mjpeg", AttachedPic: true},
				}}, nil
			})
		if err != nil {
			t.Fatal(err)
		}
		if class != video.MediaAudio {
			t.Fatalf("class = %q; want %q", class, video.MediaAudio)
		}
	})

	t.Run("true video", func(t *testing.T) {
		class, err := classifyUploadedMedia(context.Background(), true,
			"clip.mp4", "video/mp4", "/tmp/clip.mp4",
			func(_ context.Context, _ string) (video.MediaProbe, error) {
				return video.MediaProbe{Streams: []video.MediaStream{
					{Index: 0, CodecType: "video", CodecName: "h264", Width: 1920, Height: 1080},
					{Index: 1, CodecType: "audio", CodecName: "aac"},
				}}, nil
			})
		if err != nil {
			t.Fatal(err)
		}
		if class != video.MediaVideo {
			t.Fatalf("class = %q; want %q", class, video.MediaVideo)
		}
	})

	t.Run("unsupported file", func(t *testing.T) {
		class, err := classifyUploadedMedia(context.Background(), true,
			"data.bin", "application/octet-stream", "/tmp/data.bin",
			func(_ context.Context, _ string) (video.MediaProbe, error) {
				return video.MediaProbe{}, nil
			})
		if err != nil {
			t.Fatal(err)
		}
		if class != video.MediaUnsupported {
			t.Fatalf("class = %q; want %q", class, video.MediaUnsupported)
		}
	})

	t.Run("disabled rejects audio", func(t *testing.T) {
		probeCalled := false
		class, err := classifyUploadedMedia(context.Background(), false,
			"song.mp3", "audio/mpeg", "/tmp/song.mp3",
			func(_ context.Context, _ string) (video.MediaProbe, error) {
				probeCalled = true
				return video.MediaProbe{}, nil
			})
		if err != nil {
			t.Fatal(err)
		}
		if class != video.MediaUnsupported {
			t.Fatalf("class = %q; want %q", class, video.MediaUnsupported)
		}
		if probeCalled {
			t.Fatal("expected probe not to be called when disabled")
		}
	})
}
