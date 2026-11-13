package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"net/http"
	"strings"
)

//go:embed panel.html
var panel []byte

const managementBase = "plugins/" + pluginID

func managementRoutes() any {
	routes := []pluginapi.ManagementRoute{}
	for _, r := range []struct{ method, path string }{{"GET", "status"}, {"GET", "models"}, {"POST", "import"}, {"POST", "account"}} {
		routes = append(routes, pluginapi.ManagementRoute{Method: r.method, Path: managementBase + "/" + r.path})
	}
	return map[string]any{"routes": routes, "resources": []pluginapi.ResourceRoute{{Path: "accounts", Menu: "Gemini 网页账号", Description: "Cookie / StorageState import and account status"}}}
}
func jsonManagement(status int, body any) pluginapi.ManagementResponse {
	b, _ := json.Marshal(body)
	return pluginapi.ManagementResponse{StatusCode: status, Headers: http.Header{"Content-Type": []string{"application/json"}, "Cache-Control": []string{"no-store"}}, Body: b}
}
func (r *Runtime) management(raw []byte) (pluginapi.ManagementResponse, error) {
	var req pluginapi.ManagementRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return jsonManagement(400, map[string]string{"error": "invalid request"}), nil
	}
	// Resource routes are PUBLIC. Only static assets can be returned there.
	if req.Method == "GET" && strings.Contains(req.Path, "/resource/plugins/"+pluginID+"/") && strings.HasSuffix(req.Path, "/accounts") {
		return pluginapi.ManagementResponse{StatusCode: 200, Headers: http.Header{"Content-Type": []string{"text/html; charset=utf-8"}, "Cache-Control": []string{"no-store"}}, Body: panel}, nil
	}
	// The CPA host authenticates all routes registered under managementBase.
	parts := strings.Split(strings.Trim(req.Path, "/"), "/")
	route := parts[len(parts)-1]
	switch {
	case req.Method == "GET" && route == "status":
		return jsonManagement(200, r.statusView()), nil
	case req.Method == "GET" && route == "models":
		return jsonManagement(200, r.models()), nil
	case req.Method == "POST" && route == "import":
		if len(req.Body) > 4<<20 {
			return jsonManagement(413, map[string]string{"error": "account import exceeds 4 MiB"}), nil
		}
		var body struct {
			Cookie string          `json:"cookie"`
			State  json.RawMessage `json:"storage_state"`
			ID     string          `json:"id"`
		}
		if json.Unmarshal(req.Body, &body) != nil {
			return jsonManagement(400, map[string]string{"error": "invalid import body"}), nil
		}
		var id string
		var err error
		ctx, cancel := context.WithTimeout(r.ctx, duration(r.cfg.InitTimeout))
		defer cancel()
		if len(body.State) > 0 && string(body.State) != "null" {
			id, err = r.manager.ImportState(body.State)
		} else if strings.TrimSpace(body.Cookie) != "" {
			id, err = r.manager.ImportCookie(ctx, body.Cookie, body.ID, "", 0)
		} else {
			return jsonManagement(400, map[string]string{"error": "cookie or storage_state is required"}), nil
		}
		if err != nil {
			return jsonManagement(400, map[string]string{"error": "account import failed; verify cookies, storage state and proxy"}), nil
		}
		return jsonManagement(200, map[string]string{"id": id}), nil
	case req.Method == "POST" && route == "account":
		var body struct {
			ID     string `json:"id"`
			Action string `json:"action"`
		}
		if json.Unmarshal(req.Body, &body) != nil || body.ID == "" {
			return jsonManagement(400, map[string]string{"error": "account id is required"}), nil
		}
		var err error
		switch body.Action {
		case "enable":
			err = r.manager.SetEnabled(body.ID, true)
		case "disable":
			err = r.manager.SetEnabled(body.ID, false)
		case "verify":
			err = r.manager.Verify(body.ID)
		case "remove":
			err = r.manager.Remove(body.ID)
		default:
			return jsonManagement(400, map[string]string{"error": "unknown account action"}), nil
		}
		if err != nil {
			return jsonManagement(400, map[string]string{"error": "account operation failed"}), nil
		}
		return jsonManagement(200, map[string]bool{"ok": true}), nil
	}
	return jsonManagement(404, map[string]string{"error": "unknown plugin route"}), nil
}

// Do not serialize the full upstream status object: it can include proxy URLs or raw errors.
func (r *Runtime) statusView() any {
	s := r.pool.Status()
	views := []map[string]any{}
	for _, a := range s.Accounts {
		views = append(views, map[string]any{"id": a.AccountID, "state": a.State, "enabled": a.Enabled, "tier": a.Tier, "models": a.Models, "pending": a.Pending, "cooldown_until": a.CooldownUntil})
	}
	out := map[string]any{"enabled": true, "model_prefix": r.cfg.ModelPrefix, "total": s.Total, "available": s.Available, "accounts": views}
	if s.Guest != nil {
		out["guest"] = map[string]any{"state": s.Guest.State, "models": s.Guest.Models}
	}
	return out
}
