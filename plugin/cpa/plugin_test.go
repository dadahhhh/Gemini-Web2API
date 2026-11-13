package main

import (
	"encoding/json"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	e := &Engine{}
	cfg, _ := json.Marshal(map[string]any{"config_yaml": []byte("data_dir: " + t.TempDir() + "\nguest_enabled: false\n")})
	if _, err := e.Handle("plugin.register", cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	return e
}
func TestExplicitRoutingAndUnrelatedModels(t *testing.T) {
	e := newTestEngine(t)
	for model, expected := range map[string]bool{"gemini-web/gemini-3.8-flash": true, "gemini-3.8-flash-high": false, "gpt-6.1-sol": false, "gemini-web/": false} {
		raw, _ := json.Marshal(pluginapi.ModelRouteRequest{RequestedModel: model})
		value, err := e.Handle("model.route", raw)
		if err != nil {
			t.Fatal(err)
		}
		if value.(pluginapi.ModelRouteResponse).Handled != expected {
			t.Fatal(model)
		}
	}
}
func TestRegistrationModelsAndProtectedManagement(t *testing.T) {
	e := newTestEngine(t)
	value, err := e.Handle("model.register", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range value.(pluginapi.ModelRegistrationResponse).Models {
		if !strings.HasPrefix(m.ID, "gemini-web/") {
			t.Fatal(m.ID)
		}
	}
	raw, _ := json.Marshal(pluginapi.ManagementRequest{Method: "GET", Path: "/v0/resource/plugins/" + pluginID + "/accounts"})
	value, err = e.Handle("management.handle", raw)
	if err != nil {
		t.Fatal(err)
	}
	res := value.(pluginapi.ManagementResponse)
	if res.StatusCode != http.StatusOK || !strings.Contains(string(res.Body), "<html") {
		t.Fatal("panel unavailable")
	}
	if strings.Contains(string(res.Body), "storage-state.json\":") {
		t.Fatal("private data in public resource")
	}
	raw, _ = json.Marshal(pluginapi.ManagementRequest{Method: "POST", Path: "/v0/management/plugins/" + pluginID + "/import", Body: []byte(`{}`)})
	value, err = e.Handle("management.handle", raw)
	if err != nil {
		t.Fatal(err)
	}
	if value.(pluginapi.ManagementResponse).StatusCode != 400 {
		t.Fatal("empty import accepted")
	}
}
func TestDisabledPluginDeclinesRouting(t *testing.T) {
	e := &Engine{}
	defer e.Close()
	raw, _ := json.Marshal(map[string]any{"config_yaml": []byte("enabled: false\n")})
	if _, err := e.Handle("plugin.register", raw); err != nil {
		t.Fatal(err)
	}
	req, _ := json.Marshal(pluginapi.ModelRouteRequest{RequestedModel: "gemini-web/x"})
	out, err := e.Handle("model.route", req)
	if err != nil || out.(pluginapi.ModelRouteResponse).Handled {
		t.Fatal("disabled plugin routed request")
	}
}

func TestExecutorStreamUsesHostCallbacksAndPreservesBytes(t *testing.T) {
	e := newTestEngine(t)
	release := make(chan struct{})
	defer close(release)
	first := "data: {\"text\":\"你好\"}\n\n"
	last := "data: [DONE]\n\n"
	e.runtime.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, first)
		<-release
		io.WriteString(w, last)
	})
	chunks := make(chan []byte, 4)
	closed := make(chan struct{})
	e.host = func(method string, payload any) error {
		m := payload.(map[string]any)
		switch method {
		case "host.stream.emit":
			chunks <- m["payload"].([]byte)
		case "host.stream.close":
			close(closed)
		}
		return nil
	}
	raw, _ := json.Marshal(map[string]any{"Model": "gemini-web/gemini-3.8-flash", "Format": "openai", "Payload": []byte(`{"messages":[]}`), "stream_id": "stream-1"})
	if _, err := e.Handle("executor.execute_stream", raw); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-chunks:
		if string(got) != first {
			t.Fatal("SSE bytes changed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first event was buffered")
	}
	// The first event must arrive while the embedded handler is still waiting.
	release <- struct{}{}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("host stream did not close")
	}
	if string(<-chunks) != last {
		t.Fatal("DONE marker changed")
	}
}

func TestStreamErrorRetainsHTTPStatus(t *testing.T) {
	e := newTestEngine(t)
	e.runtime.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(429) })
	closed := make(chan any, 1)
	e.host = func(method string, payload any) error {
		if method == "host.stream.close" {
			closed <- payload
		}
		return nil
	}
	raw, _ := json.Marshal(map[string]any{"Model": "gemini-web/x", "Format": "openai", "Payload": []byte(`{}`), "stream_id": "stream-2"})
	if _, err := e.Handle("executor.execute_stream", raw); err != nil {
		t.Fatal(err)
	}
	select {
	case value := <-closed:
		b, _ := json.Marshal(value)
		if !strings.Contains(string(b), `"http_status":429`) {
			t.Fatalf("status lost: %s", b)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("error not returned")
	}
}
