package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

const completed = `data: {"type":"response.completed","response":{"id":"r","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}` + "\n\n"

// A token revoked before its expiry is refreshed once on a 401 and the step
// goes through; a refusal of the refresh asks for a new sign-in.
func TestChatGPTRefreshOn401(t *testing.T) {
	for _, refused := range []bool{false, true} {
		var refreshes, calls atomic.Int32
		auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			refreshes.Add(1)
			if refused {
				http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"new","refresh_token":"r2","expires_in":3600}`))
		}))
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if r.Header.Get("Authorization") != "Bearer new" {
				http.Error(w, `{"error":{"code":"token_expired"}}`, http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(completed))
		}))
		tokenURL = auth.URL
		dir := t.TempDir()
		if err := saveCreds(dir, chatgptCreds{ClientID: "c", AccessToken: "old", RefreshToken: "r1", Expires: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		p := ChatGPT(api.URL, dir)
		_, err := p.Step(context.Background(), &Conn{}, Request{Model: "m", Context: []statefile.Record{{Header: statefile.Header{Kind: statefile.Message, Type: "text/plain"}, Body: []byte("hi")}}}, func(statefile.Record) {})
		switch {
		case refused && (err == nil || !strings.Contains(err.Error(), "mainplane signin")):
			t.Fatalf("refused refresh: %v", err)
		case !refused && err != nil:
			t.Fatal(err)
		case refreshes.Load() != 1 || calls.Load() != map[bool]int32{false: 2, true: 1}[refused]:
			t.Fatalf("refused %v: %d refreshes, %d calls", refused, refreshes.Load(), calls.Load())
		}
		if !refused { // a stale token another call already replaced is not refreshed again
			if k, err := chatgptToken(context.Background(), dir, "old"); err != nil || k != "new" || refreshes.Load() != 1 {
				t.Fatalf("%q %v, %d refreshes", k, err, refreshes.Load())
			}
		}
		auth.Close()
		api.Close()
	}
}
