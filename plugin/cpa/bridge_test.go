package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRequestProtocolsAndCredentialBoundary(t *testing.T) {
	cfg, _ := parseConfig(nil)
	for format, path := range map[string]string{"openai": "/v1/chat/completions", "openai-response": "/v1/responses", "claude": "/v1/messages", "gemini": "/v1beta/models/gemini-3.8-flash:generateContent"} {
		t.Run(format, func(t *testing.T) {
			in := pluginapi.ExecutorRequest{Model: "gemini-web/gemini-3.8-flash", Format: format, Payload: []byte(`{"messages":[],"model":"wrong"}`), Headers: http.Header{"Authorization": []string{"Bearer private"}, "Cookie": []string{"secret"}, "X-Session-Id": []string{"session-1"}}}
			req, err := requestFor(cfg, in, false)
			if err != nil {
				t.Fatal(err)
			}
			if req.URL.Path != path {
				t.Fatal(req.URL.Path)
			}
			if req.Header.Get("Authorization") != "" || req.Header.Get("Cookie") != "" {
				t.Fatal("credential crossed embedded boundary")
			}
			var body map[string]any
			json.NewDecoder(req.Body).Decode(&body)
			if body["model"] != "gemini-3.8-flash" {
				t.Fatal(body)
			}
		})
	}
	if _, err := requestFor(cfg, pluginapi.ExecutorRequest{Model: "gemini-3.8-flash", Format: "openai", Payload: []byte(`{}`)}, false); err == nil {
		t.Fatal("unprefixed route accepted")
	}
	if _, err := requestFor(cfg, pluginapi.ExecutorRequest{Model: "gemini-web/x", Format: "openai", Payload: []byte(`[]`)}, false); err == nil {
		t.Fatal("nonobject accepted")
	}
}

func TestSSEIsForwardedBeforeHandlerCompletes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release := make(chan struct{})
	defer close(release)
	first := "data: {\"text\":\"你好\"}\n\n"
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, first)
		w.(http.Flusher).Flush()
		select {
		case <-release:
			io.WriteString(w, "data: [DONE]\n\n")
		case <-r.Context().Done():
		}
	})
	req, _ := http.NewRequest("POST", "http://embedded.invalid/", nil)
	response, err := servePipe(ctx, h, req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	got := make(chan string, 1)
	go func() {
		buf := make([]byte, len(first))
		_, err := io.ReadFull(response.Body, buf)
		if err != nil {
			got <- err.Error()
		} else {
			got <- string(buf)
		}
	}()
	select {
	case s := <-got:
		if s != first {
			t.Fatalf("SSE changed: %q", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stream was buffered until completion")
	}
}

func TestPipeCancellationUnblocksAStalledWriter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		w.(http.Flusher).Flush()
		io.WriteString(w, strings.Repeat("x", 100000))
	})
	req, _ := http.NewRequest("POST", "http://embedded.invalid/", nil)
	res, err := servePipe(ctx, h, req)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	res.Body.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("canceled stream leaked handler")
	}
}

func TestUpstreamErrorsDoNotExposeSecrets(t *testing.T) {
	err := responseError(429, []byte(`{"cookie":"__Secure-1PSID=private"}`))
	if strings.Contains(err.Error(), "private") {
		t.Fatal("secret exposed")
	}
	var status interface{ StatusCode() int }
	if !errors.As(err, &status) || status.StatusCode() != 429 {
		t.Fatal("HTTP status lost")
	}
}

func TestConfigValidation(t *testing.T) {
	for _, raw := range []string{"model_prefix: ''", "per_account_concurrency: 0", "request_timeout: 0s", "request_timeout: 72h", "routing_strategy: random"} {
		if _, err := parseConfig([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
