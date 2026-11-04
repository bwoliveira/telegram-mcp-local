package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func mediaTypesFromCatalog(t *testing.T, s *Server) []string {
	t.Helper()
	var listed struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				InputSchema struct {
					Properties map[string]struct {
						Enum []string `json:"enum"`
					} `json:"properties"`
				} `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	body := s.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatal(err)
	}
	for _, tool := range listed.Result.Tools {
		if tool.Name == "send_media_message" {
			return tool.InputSchema.Properties["type"].Enum
		}
	}
	t.Fatal("send_media_message missing from tools/list")
	return nil
}

func TestToolsListMediaTypesMatchValidator(t *testing.T) {
	s := New(Config{})
	want := []string{"image", "photo", "video", "audio", "voice", "document", "sticker", "animation"}
	if got := mediaTypesFromCatalog(t, s); !reflect.DeepEqual(got, want) {
		t.Fatalf("tools/list media types = %v; validator accepts %v", got, want)
	}
}

type mediaCatalogEngine struct {
	engine.Controller
	request engine.SendMediaRequest
}

func (e *mediaCatalogEngine) SendMedia(ctx context.Context, request engine.SendMediaRequest) (engine.WriteResult, error) {
	e.request = request
	return e.Controller.SendMedia(ctx, request)
}

func TestToolsListMediaTypesReachPreviewWithoutWrites(t *testing.T) {
	kinds := map[string]engine.MediaType{
		"image": engine.MediaPhoto, "photo": engine.MediaPhoto,
		"video": engine.MediaVideo, "audio": engine.MediaAudio,
		"voice": engine.MediaVoice, "document": engine.MediaDocument,
		"sticker": engine.MediaSticker, "animation": engine.MediaAnimation,
	}
	for _, advertised := range mediaTypesFromCatalog(t, New(Config{})) {
		t.Run(advertised, func(t *testing.T) {
			s, fake := newFakeServer(t, engine.ModeReadWrite)
			capture := &mediaCatalogEngine{Controller: fake}
			s.eng = capture
			path := filepath.Join(t.TempDir(), "synthetic.webp")
			// Only the local sticker header is validated; no decoder or upload is used.
			if err := os.WriteFile(path, []byte("RIFF\x04\x00\x00\x00WEBP"), 0o600); err != nil {
				t.Fatal(err)
			}
			result := toolCall(t, s, "send_media_message", map[string]any{
				"to": "user:1001", "type": advertised, "url": path, "dry_run": true,
			})
			if fp := previewFingerprint(t, result); fp == "" {
				t.Fatal("media preview has no approval fingerprint")
			}
			want, ok := kinds[advertised]
			if !ok || capture.request.Kind != want || !capture.request.DryRun {
				t.Fatalf("catalog type %q did not reach its dry-run engine kind: %+v", advertised, capture.request)
			}
			if len(fake.Writes()) != 0 {
				t.Fatal("catalog validation or preview wrote to the Telegram engine")
			}
		})
	}
}
