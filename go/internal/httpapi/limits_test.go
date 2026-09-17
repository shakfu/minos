package httpapi

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The multipart parser must pass the limit's error through, or an oversized
// upload reads as a missing field.
func TestAnUploadPastTheLimitIsTooLarge(t *testing.T) {
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	_ = form.WriteField("path", "home:/big")
	part, _ := form.CreateFormFile("upload", "upload")
	_, _ = part.Write(bytes.Repeat([]byte("x"), 4096))
	_ = form.Close()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/vfs/writefile", &body)
	request.Header.Set("Content-Type", form.FormDataContentType())
	request.Body = http.MaxBytesReader(recorder, request.Body, 1024)

	err := request.ParseMultipartForm(512)
	if !tooLarge(recorder, err) {
		t.Fatalf("the parser's error %v was not taken for the limit", err)
	}
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("answered %d", recorder.Code)
	}
}
