package api

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gophish/gophish/models"
)

// TestPutTemplateRejectsAnUndecodableBody asserts that a request body which
// fails to decode is refused rather than partially applied.
//
// The PUT handler logged the decode error and carried on with whatever had been
// decoded so far. encoding/json fills in every field it can and reports a type
// error at the end, so a body whose attachments field has the wrong type
// produced a Template with the right id and name and no attachments at all.
// PutTemplate deletes every attachment before re-inserting the ones it was
// given, so the handler silently destroyed them and answered 200.
func TestPutTemplateRejectsAnUndecodableBody(t *testing.T) {
	ctx := setupTest(t)

	template := models.Template{
		Name:    "Attachment Template",
		Subject: "Test subject",
		Text:    "Text text",
		HTML:    "<html>Test</html>",
		UserId:  1,
		Attachments: []models.Attachment{
			{Name: "notes.txt", Type: "text/plain", Content: "dGVzdA=="},
		},
	}
	if err := models.PostTemplate(&template); err != nil {
		t.Fatalf("error creating the template: %v", err)
	}

	// Valid JSON, but attachments is a string rather than an array.
	body := fmt.Sprintf(`{"id":%d,"name":"Attachment Template","subject":"Test subject",`+
		`"text":"Text text","html":"<html>Test</html>","attachments":"not-an-array"}`,
		template.Id)

	url := fmt.Sprintf("/api/templates/%d", template.Id)
	r := httptest.NewRequest(http.MethodPut, url, bytes.NewBufferString(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", fmt.Sprintf("Bearer %s", ctx.apiKey))
	w := httptest.NewRecorder()
	ctx.apiServer.ServeHTTP(w, r)

	if w.Code == http.StatusOK {
		t.Errorf("a template PUT whose body failed to decode was accepted with 200")
	}

	got, err := models.GetTemplate(template.Id, 1)
	if err != nil {
		t.Fatalf("error reading the template back: %v", err)
	}
	if len(got.Attachments) != 1 {
		t.Fatalf("the template kept %d of its 1 attachment: a request that never "+
			"decoded destroyed them", len(got.Attachments))
	}
}
