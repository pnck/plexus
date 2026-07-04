package agent

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// rawObsMiddleware mirrors the request body to the obs sink and restores it so
// the downstream call still sees an intact body. Headers (the API key) are not
// emitted.
func TestRawObsMiddlewareEmitsAndRestoresBody(t *testing.T) {
	var emitted []string
	mw := rawObsMiddleware(func(b []byte) { emitted = append(emitted, string(b)) })

	req, _ := http.NewRequest(http.MethodPost, "https://api.example/v1/chat", strings.NewReader(`{"model":"x"}`))
	req.Header.Set("Authorization", "Bearer secret-key")

	var seenBody string
	next := func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		seenBody = string(b)
		return &http.Response{Status: "200 OK"}, nil
	}

	if _, err := mw(req, next); err != nil {
		t.Fatalf("middleware: %v", err)
	}
	if seenBody != `{"model":"x"}` {
		t.Fatalf("downstream body not restored: %q", seenBody)
	}
	all := strings.Join(emitted, "\n")
	if !strings.Contains(all, `"model"`) || !strings.Contains(all, "200 OK") {
		t.Fatalf("raw obs did not capture request+response: %q", emitted)
	}
	if strings.Contains(all, "secret-key") {
		t.Fatalf("raw obs leaked the auth header: %q", emitted)
	}
}
