package gemini

import (
	"context"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	http "github.com/bogdanfinn/fhttp"
)

func TestIdentityPolicyReachesWireWithoutChangingCredentials(t *testing.T) {
	got := make(chan stdhttp.Header, 1)
	server := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) { got <- r.Header.Clone(); w.WriteHeader(200) }))
	defer server.Close()
	profile := BrowserIdentityHeaders("156.0.1.2", time.Now())
	profile["x-sec-ch-ua-not-a-border"] = "expired-token"
	profile["cookie"] = "forbidden"
	profile["authorization"] = "forbidden"
	profile["x-goog-ext-525001261-jspb"] = "forbidden"
	req, err := http.NewRequestWithContext(WithBrowserIdentity(context.Background(), profile), "POST", server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Cookie", "synthetic-cookie")
	req.Header.Set("Authorization", "synthetic-auth")
	req.Header.Set("x-goog-ext-525001261-jspb", "synthetic-model")
	applyChromeHeaders(req, "zh-CN", true)
	client, err := NewHTTPClient(2, "")
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	h := <-got
	if h.Get("X-Sec-Ch-Ua-Not-A-Border") == "expired-token" || len(h.Get("X-Sec-Ch-Ua-Not-A-Border")) != 32 {
		t.Fatal("queued identity token was not refreshed")
	}
	if !strings.Contains(h.Get("User-Agent"), "Chrome/156.") || h.Get("Sec-Ch-Ua-Full-Version") != `"156.0.1.2"` {
		t.Fatal("policy not applied to actual request")
	}
	if h.Get("Cookie") != "synthetic-cookie" || h.Get("Authorization") != "synthetic-auth" || h.Get("x-goog-ext-525001261-jspb") != "synthetic-model" {
		t.Fatal("identity policy changed credentials or model routing")
	}
	if h.Get("X-Cpa-Web-Gemini-Identity") != "" {
		t.Fatal("internal envelope leaked upstream")
	}
	plain, _ := http.NewRequest("GET", server.URL, nil)
	applyChromeHeaders(plain, "en-US", true)
	if plain.Header.Get("User-Agent") != ChromeUserAgent {
		t.Fatal("policy leaked into unrelated request context")
	}
}
func TestIdentityPolicyIgnoresNonGoogleRequests(t *testing.T) {
	ctx := WithBrowserIdentity(context.Background(), map[string]string{"user-agent": "policy-agent"})
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://example.com/file", nil)
	applyChromeHeaders(req, "en-US", false)
	if req.Header.Get("User-Agent") != ChromeUserAgent {
		t.Fatal("Google identity affected non-Google download")
	}
}
