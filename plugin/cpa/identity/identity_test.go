package main

import (
	"encoding/json"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"strings"
	"testing"
)

func configured(t *testing.T, cfg string) *Engine {
	t.Helper()
	e := &Engine{}
	raw, _ := json.Marshal(map[string]any{"config_yaml": []byte(cfg)})
	if _, err := e.Handle("plugin.register", raw); err != nil {
		t.Fatal(err)
	}
	return e
}
func TestOnlyWebGeminiModelsReceivePolicy(t *testing.T) {
	e := configured(t, "")
	for model, want := range map[string]bool{"gemini-web/gemini-4-hydrogen": true, "web/Gemini-4-argon": true, "gemini-3.8-flash-high": false, "gpt-6.1-sol": false, "claude-sonnet-5-5": false, "web/gpt-6": false, "gemini-web/": false} {
		raw, _ := json.Marshal(pluginapi.RequestInterceptRequest{Model: model, RequestedModel: model, Body: []byte("synthetic-private-body")})
		v, err := e.Handle("request.intercept_after", raw)
		if err != nil {
			t.Fatal(err)
		}
		r := v.(pluginapi.RequestInterceptResponse)
		if (r.Headers.Get(policyHeader) != "") != want {
			t.Fatal(model)
		}
		encoded, _ := json.Marshal(r)
		if strings.Contains(string(encoded), "synthetic-private-body") || len(r.Body) != 0 || r.Terminate {
			t.Fatal("interceptor affected payload")
		}
	}
}
func TestDisabledIdentityAndInvalidProfile(t *testing.T) {
	e := configured(t, "enabled: false\n")
	raw, _ := json.Marshal(pluginapi.RequestInterceptRequest{Model: "gemini-web/gemini-4-hydrogen"})
	r, _ := e.Handle("request.intercept_before", raw)
	if len(r.(pluginapi.RequestInterceptResponse).Headers) != 0 {
		t.Fatal("disabled policy applied")
	}
	raw, _ = json.Marshal(map[string]any{"config_yaml": []byte("chrome_version: '157.0.1.2 injected'\n")})
	if _, err := e.Handle("plugin.reconfigure", raw); err == nil {
		t.Fatal("invalid version accepted")
	}
}
func TestIdentityIsFreshAndContainsNoAccountData(t *testing.T) {
	e := configured(t, "chrome_version: 156.0.1.2\n")
	raw, _ := json.Marshal(pluginapi.RequestInterceptRequest{Model: "gemini-web/gemini-4-hydrogen"})
	v, _ := e.Handle("request.intercept_after", raw)
	r := v.(pluginapi.RequestInterceptResponse)
	var profile struct {
		Version int               `json:"version"`
		Headers map[string]string `json:"headers"`
	}
	if json.Unmarshal([]byte(r.Headers.Get(policyHeader)), &profile) != nil {
		t.Fatal("invalid envelope")
	}
	if profile.Version != 1 || !strings.Contains(profile.Headers["user-agent"], "Chrome/156.") || len(profile.Headers["x-sec-ch-ua-not-a-border"]) != 32 {
		t.Fatal("identity incomplete")
	}
	for _, k := range []string{"cookie", "authorization", "x-goog-ext-525001261-jspb"} {
		if _, ok := profile.Headers[k]; ok {
			t.Fatal("private or routing field in policy")
		}
	}
}
