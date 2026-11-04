package gotdengine

import (
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"testing"
)

func TestMediaExtensionInventoryClassification(t *testing.T) {
	cases := []struct {
		kind             engine.MediaType
		mime, name, want string
	}{
		{engine.MediaPhoto, "image/jpeg", "../../bad.exe", ".jpg"},
		{engine.MediaAudio, "audio/mpeg", "bad.exe", ".mp3"},
		{engine.MediaVoice, "audio/ogg", "", ".ogg"},
		{engine.MediaVideo, "video/mp4", "../../bad.exe", ".mp4"},
		{engine.MediaVideoNote, "", "", ".mp4"},
		{engine.MediaAnimation, "", "", ".mp4"},
		{engine.MediaSticker, "image/webp", "", ".webp"},
		{engine.MediaDocument, "application/octet-stream", "../../bad.jpg", ".bin"},
	}
	for _, c := range cases {
		if got := mediaExtension(mediaFile{kind: c.kind, mime: c.mime, filename: c.name}); got != c.want {
			t.Errorf("%s/%s: %s != %s", c.kind, c.mime, got, c.want)
		}
	}
}
