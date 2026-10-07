package gmail

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/googleapi"
)

func TestGetAttachmentAfterIDsRotate(t *testing.T) {
	for _, root := range []bool{false, true} {
		for _, inline := range []bool{false, true} {
			t.Run(fmt.Sprintf("root=%t/inline=%t", root, inline), func(t *testing.T) {
				var fetches atomic.Int32
				data := []byte{0, 1, 2, 255}
				s := &Service{svc: fakeGmailUsersService(t, func(w http.ResponseWriter, r *http.Request) {
					switch {
					case strings.HasSuffix(r.URL.Path, "/messages/m1"):
						n := fetches.Add(1)
						part := &gmail.MessagePart{PartId: "2.0", Filename: "image.png", MimeType: "image/png",
							Body: &gmail.MessagePartBody{AttachmentId: fmt.Sprintf("fresh-%d", n), Size: int64(len(data))}}
						if inline {
							part.Body.AttachmentId = ""
							part.Body.Data = base64.RawURLEncoding.EncodeToString(data)
						}
						if root {
							part.PartId = ""
						} else {
							part = &gmail.MessagePart{MimeType: "multipart/mixed", Parts: []*gmail.MessagePart{
								{PartId: "2", MimeType: "multipart/related", Parts: []*gmail.MessagePart{part}},
							}}
						}
						writeJSON(t, w, &gmail.Message{Id: "m1", Payload: part})
					case !inline && strings.HasSuffix(r.URL.Path, "/messages/m1/attachments/fresh-2"):
						writeJSON(t, w, &gmail.MessagePartBody{Data: base64.RawURLEncoding.EncodeToString(data)})
					default:
						t.Errorf("unexpected request %s", r.URL.Path)
						http.NotFound(w, r)
					}
				})}
				detail, err := s.GetEmail(context.Background(), "m1")
				if err != nil || len(detail.Attachments) != 1 {
					t.Fatalf("read: detail=%+v err=%v", detail, err)
				}
				att, err := s.GetAttachment(context.Background(), "m1", detail.Attachments[0].AttachmentID)
				if err != nil {
					t.Fatal(err)
				}
				if string(att.Data) != string(data) || att.Filename != "image.png" || att.MimeType != "image/png" || fetches.Load() != 2 {
					t.Fatalf("download = %+v, fetches = %d", att, fetches.Load())
				}
			})
		}
	}
}

func TestGetAttachmentLegacyIDFallback(t *testing.T) {
	for _, status := range []int{200, 403, 404} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			s := &Service{svc: fakeGmailUsersService(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/messages/m1"):
					// The current payload no longer mentions the old ID. Its metadata
					// must not be assigned to the bytes fetched by that old ID.
					writeJSON(t, w, &gmail.Message{Payload: &gmail.MessagePart{Filename: "other.pdf",
						PartId: "1", MimeType: "application/pdf", Body: &gmail.MessagePartBody{AttachmentId: "new-id"}}})
				case strings.HasSuffix(r.URL.Path, "/messages/m1/attachments/legacy-id"):
					if status != 200 {
						writeGoogleAPIError(t, w, status, "old attachment unavailable")
					} else {
						writeJSON(t, w, &gmail.MessagePartBody{Data: base64.RawURLEncoding.EncodeToString([]byte("legacy bytes"))})
					}
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					http.NotFound(w, r)
				}
			})}
			att, err := s.GetAttachment(context.Background(), "m1", "legacy-id")
			if status != 200 {
				var apiErr *googleapi.Error
				if !errors.As(err, &apiErr) || apiErr.Code != status || att != nil {
					t.Fatalf("want preserved API error %d, got %+v, %v", status, att, err)
				}
				return
			}
			if err != nil || string(att.Data) != "legacy bytes" || att.Filename != "attachment.bin" || att.MimeType != "application/octet-stream" {
				t.Fatalf("legacy download = %+v, %v", att, err)
			}
		})
	}
}
