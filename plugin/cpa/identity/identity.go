package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mag1cFall/Gemini-Web2API/internal/gemini"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

const pluginID = "web-gemini-identity"
const policyHeader = "X-Cpa-Web-Gemini-Identity"
const version = "0.1.0"

type Config struct {
	Enabled       bool   `yaml:"enabled"`
	ChromeVersion string `yaml:"chrome_version"`
}
type Engine struct {
	mu     sync.RWMutex
	cfg    Config
	host   func(string, any) error
	before atomic.Uint64
	after  atomic.Uint64
}

func (e *Engine) Close() { e.mu.Lock(); e.cfg.Enabled = false; e.mu.Unlock() }
func registration() any {
	return map[string]any{"schema_version": 10, "metadata": pluginapi.Metadata{Name: pluginID, Version: version, Author: "dadahhhh; Chrome identity from Mag1cFall/Gemini-Web2API", GitHubRepository: "https://github.com/dadahhhh/Gemini-Web2API", ConfigFields: []pluginapi.ConfigField{{Name: "enabled", Type: "boolean", Default: true}, {Name: "chrome_version", Type: "string", Default: "157.0.8103.40", Description: "Browser UA and client hints; TLS profile remains owned by Gemini Web transport"}}}, "capabilities": map[string]any{"request_interceptor": true, "request_interceptor_omit_body_before": true, "request_interceptor_omit_body_after": true, "management_api": true}}
}
func scoped(model string) bool {
	for _, prefix := range []string{"gemini-web/", "web/"} {
		suffix, ok := strings.CutPrefix(model, prefix)
		if ok && strings.HasPrefix(strings.ToLower(suffix), "gemini-") {
			return true
		}
	}
	return false
}
func (e *Engine) Handle(method string, raw []byte) (any, error) {
	switch method {
	case "plugin.register", "plugin.reconfigure":
		var request struct {
			Config []byte `json:"config_yaml"`
		}
		if err := json.Unmarshal(raw, &request); err != nil {
			return nil, err
		}
		cfg := Config{Enabled: true, ChromeVersion: "157.0.8103.40"}
		if err := yaml.Unmarshal(request.Config, &cfg); err != nil {
			return nil, err
		}
		if !regexp.MustCompile(`^[1-9][0-9]{2}\.[0-9]{1,6}\.[0-9]{1,6}\.[0-9]{1,6}$`).MatchString(cfg.ChromeVersion) {
			return nil, fmt.Errorf("invalid chrome_version")
		}
		e.mu.Lock()
		e.cfg = cfg
		e.mu.Unlock()
		return registration(), nil
	case "plugin.quiesce", "plugin.shutdown":
		e.Close()
		return map[string]any{}, nil
	case "request.intercept_before", "request.intercept_after":
		var request pluginapi.RequestInterceptRequest
		if err := json.Unmarshal(raw, &request); err != nil {
			return nil, err
		}
		e.mu.RLock()
		cfg := e.cfg
		e.mu.RUnlock()
		if !cfg.Enabled || (!scoped(request.Model) && !scoped(request.RequestedModel)) {
			return pluginapi.RequestInterceptResponse{}, nil
		}
		policy, _ := json.Marshal(map[string]any{"version": 1, "headers": gemini.BrowserIdentityHeaders(cfg.ChromeVersion, time.Now())})
		if method == "request.intercept_before" {
			e.before.Add(1)
		} else {
			e.after.Add(1)
		}
		return pluginapi.RequestInterceptResponse{ClearHeaders: []string{policyHeader}, Headers: http.Header{policyHeader: []string{string(policy)}}}, nil
	case "management.register":
		return map[string]any{"routes": []pluginapi.ManagementRoute{{Method: "GET", Path: "plugins/" + pluginID + "/status"}}, "resources": []pluginapi.ResourceRoute{{Path: "about", Menu: "网页 Gemini 身份", Description: "Web-only browser identity policy"}}}, nil
	case "management.handle":
		var req pluginapi.ManagementRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		if req.Method == "GET" && req.Path == "/v0/resource/plugins/"+pluginID+"/about" {
			return pluginapi.ManagementResponse{StatusCode: 200, Headers: http.Header{"Content-Type": []string{"text/html; charset=utf-8"}, "Cache-Control": []string{"no-store"}}, Body: []byte(`<!doctype html><html lang="zh-CN"><meta charset="utf-8"><title>网页 Gemini 身份</title><h1>网页 Gemini 身份插件</h1><p>web-gemini-identity · v0.1.0</p><p>仅作用于 gemini-web/gemini-* 和 web/gemini-*。可在 CPA 插件设置中独立启停并设置 Chrome 版本。</p><p>Cookie、账号权限、TLS 指纹与模型选择由网页客户端处理。</p></html>`)}, nil
		}
		if req.Method != "GET" || req.Path != "/v0/management/plugins/"+pluginID+"/status" {
			return pluginapi.ManagementResponse{StatusCode: 404}, nil
		}
		e.mu.RLock()
		cfg := e.cfg
		e.mu.RUnlock()
		body, _ := json.Marshal(map[string]any{"plugin": pluginID, "version": version, "enabled": cfg.Enabled, "chrome_version": cfg.ChromeVersion, "scopes": []string{"gemini-web/gemini-*", "web/gemini-*"}, "before_calls": e.before.Load(), "after_calls": e.after.Load(), "tls_profile": "upstream Chrome_152, unchanged", "requires_web_plugin": "0.2.1 or newer"})
		return pluginapi.ManagementResponse{StatusCode: 200, Headers: http.Header{"Content-Type": []string{"application/json"}, "Cache-Control": []string{"no-store"}}, Body: body}, nil
	default:
		return nil, fmt.Errorf("unsupported method")
	}
}
