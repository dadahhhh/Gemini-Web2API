package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type HostCall func(string, any) error
type Engine struct {
	mu      sync.Mutex
	runtime *Runtime
	cfg     Config
	host    HostCall
}

func registration() any {
	fields := []pluginapi.ConfigField{
		{Name: "enabled", Type: "boolean", Default: true, Description: "Enable embedded Gemini Web provider"},
		{Name: "data_dir", Type: "string", Default: "data/" + pluginID, Description: "Persistent private account and request data directory"},
		{Name: "proxy", Type: "string", Default: "", Description: "Gemini Web outbound HTTP/HTTPS/SOCKS proxy"},
		{Name: "guest_enabled", Type: "boolean", Default: false, Description: "Enable anonymous guest sessions"},
		{Name: "model_prefix", Type: "string", Default: "gemini-web/", Description: "Explicit routing prefix; prevents existing provider conflicts"},
		{Name: "per_account_concurrency", Type: "integer", Default: 1, Description: "Concurrent requests per Google account (1..32)"},
		{Name: "routing_strategy", Type: "enum", Default: "round-robin", EnumValues: []string{"round-robin", "fill-first"}},
		{Name: "account_cooldown", Type: "string", Default: "2m"}, {Name: "session_ttl", Type: "string", Default: "30m"},
		{Name: "init_timeout", Type: "string", Default: "20s"}, {Name: "request_timeout", Type: "string", Default: "30m"},
	}
	return map[string]any{"schema_version": 10, "metadata": pluginapi.Metadata{Name: pluginID, Version: version, Author: "dadahhhh; upstream Mag1cFall", GitHubRepository: "https://github.com/dadahhhh/Gemini-Web2API", ConfigFields: fields},
		"capabilities": map[string]any{"model_registrar": true, "model_router": true, "executor": true, "executor_model_scope": "static", "executor_input_formats": []string{"openai", "openai-response", "claude", "gemini"}, "executor_output_formats": []string{"openai", "openai-response", "claude", "gemini"}, "management_api": true}}
}

func (e *Engine) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.runtime != nil {
		e.runtime.Close()
		e.runtime = nil
	}
}
func (e *Engine) configure(raw []byte) (any, error) {
	var req struct {
		ConfigYAML []byte `json:"config_yaml"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	cfg, err := parseConfig(req.ConfigYAML)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.runtime != nil && e.cfg == cfg {
		return registration(), nil
	}
	if e.runtime != nil {
		e.runtime.Close()
		e.runtime = nil
	}
	e.cfg = cfg
	if cfg.Enabled {
		r, err := newRuntime(cfg)
		if err != nil {
			return nil, err
		}
		e.runtime = r
	}
	return registration(), nil
}

func (e *Engine) Handle(method string, raw []byte) (any, error) {
	switch method {
	case "plugin.register", "plugin.reconfigure":
		return e.configure(raw)
	case "plugin.quiesce", "plugin.shutdown":
		e.Close()
		return map[string]any{}, nil
	case "executor.identifier":
		return map[string]string{"identifier": pluginID}, nil
	case "management.register":
		return managementRoutes(), nil
	}
	e.mu.Lock()
	r := e.runtime
	cfg := e.cfg
	if r != nil && !r.acquire() {
		r = nil
	}
	e.mu.Unlock()
	if r == nil {
		switch method {
		case "model.route":
			return pluginapi.ModelRouteResponse{}, nil
		case "model.register":
			return pluginapi.ModelRegistrationResponse{Provider: pluginID, Models: []pluginapi.ModelInfo{}}, nil
		case "management.handle":
			return jsonManagement(200, map[string]any{"enabled": false}), nil
		}
		return nil, pluginabi.NewError("unavailable", "Gemini Web plugin is disabled", 503)
	}
	if method == "executor.execute_stream" {
		var req struct {
			pluginapi.ExecutorRequest
			StreamID string `json:"stream_id"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			r.active.Done()
			return nil, err
		}
		if req.StreamID == "" || e.host == nil {
			r.active.Done()
			return nil, pluginabi.NewError("invalid_stream", "host stream callback is required", 400)
		}
		// Check request shape before reporting successful stream initialization.
		if _, err := requestFor(cfg, req.ExecutorRequest, false); err != nil {
			r.active.Done()
			return nil, err
		}
		go func() {
			defer r.active.Done()
			var runErr error
			defer func() {
				if recover() != nil {
					runErr = errors.New("embedded stream failed")
				}
				msg := ""
				if runErr != nil {
					msg = "Gemini Web stream ended with an error"
				}
				reply := map[string]any{"stream_id": req.StreamID, "error": msg}
				var detail *pluginabi.Error
				if errors.As(runErr, &detail) {
					reply["error_detail"] = detail
				}
				_ = e.host("host.stream.close", reply)
			}()
			runErr = r.stream(req.ExecutorRequest, func(b []byte) error {
				return e.host("host.stream.emit", map[string]any{"stream_id": req.StreamID, "payload": b})
			})
		}()
		return map[string]any{"headers": http.Header{"Content-Type": []string{"text/event-stream"}}}, nil
	}
	defer r.active.Done()
	switch method {
	case "model.route":
		var req pluginapi.ModelRouteRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		model, ok := strings.CutPrefix(req.RequestedModel, cfg.ModelPrefix)
		return pluginapi.ModelRouteResponse{Handled: ok && model != "", TargetKind: pluginapi.ModelRouteTargetSelf}, nil
	case "model.register":
		models := r.models()
		sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
		return pluginapi.ModelRegistrationResponse{Provider: pluginID, Models: models}, nil
	case "executor.execute", "executor.count_tokens":
		var req pluginapi.ExecutorRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		return r.execute(req, method == "executor.count_tokens")
	case "management.handle":
		return r.management(raw)
	default:
		return nil, pluginabi.NewError("not_implemented", "unsupported plugin method", 501)
	}
}
