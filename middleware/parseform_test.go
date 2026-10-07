package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestGetContextStopsOnAParseFormError asserts that GetContext stops handling a
// request once it has written an error response.
//
// It called http.Error and then carried on through the rest of the function and
// into the wrapped handler, so the handler ran against a request whose form had
// failed to parse and appended a second body to the 500 that had already been
// sent.
func TestGetContextStopsOnAParseFormError(t *testing.T) {
	setupTest(t)
	handlerRan := false
	handler := GetContext(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerRan = true
		w.Write([]byte("handler ran"))
	}))

	// An invalid percent escape makes ParseForm fail on the request body.
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("a=%zz"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)

	if handlerRan {
		t.Fatal("the wrapped handler ran after GetContext had already written a 500")
	}
	if got := response.Code; got != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", got)
	}
	if body := response.Body.String(); strings.Contains(body, "handler ran") {
		t.Fatalf("the error response has handler output appended to it: %q", body)
	}
}
