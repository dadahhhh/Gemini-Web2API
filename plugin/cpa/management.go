package main

import (
	_ "embed"
	"encoding/json"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"net/http"
	"sort"
	"strings"
)

//go:embed panel.html
var panel []byte

const managementBase = "plugins/" + pluginID

func managementRoutes() any {
	routes := []pluginapi.ManagementRoute{}
	for _, r := range []struct{ method, path string }{{"GET", "status"}, {"GET", "models"}, {"POST", "import"}, {"POST", "import-batch"}, {"POST", "account"}} {
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
		r.accountMu.Lock()
		defer r.accountMu.Unlock()
		result, err := r.importOne(req.Body)
		if err != nil {
			return jsonManagement(400, map[string]any{"error": publicImportError(err)}), nil
		}
		return jsonManagement(200, result), nil
	case req.Method == "POST" && route == "import-batch":
		if len(req.Body) > 4<<20 {
			return jsonManagement(413, map[string]string{"error": "批量导入总大小不能超过 4 MiB"}), nil
		}
		var body struct {
			Accounts []json.RawMessage `json:"accounts"`
		}
		if json.Unmarshal(req.Body, &body) != nil || len(body.Accounts) < 1 || len(body.Accounts) > 10 {
			return jsonManagement(400, map[string]string{"error": "一次请选择 1 到 10 个账号文件"}), nil
		}
		r.accountMu.Lock()
		defer r.accountMu.Unlock()
		results := []map[string]any{}
		saved := 0
		for i, raw := range body.Accounts {
			var item map[string]json.RawMessage
			if json.Unmarshal(raw, &item) != nil || item == nil {
				results = append(results, map[string]any{"index": i, "saved": false, "error": publicImportError(invalidImport("invalid_request", "账号项目格式无效"))})
				continue
			}
			item["mode"] = json.RawMessage(`"create"`)
			encoded, _ := json.Marshal(item)
			value, err := r.importOne(encoded)
			if err != nil {
				results = append(results, map[string]any{"index": i, "saved": false, "error": publicImportError(err)})
				continue
			}
			saved++
			results = append(results, map[string]any{"index": i, "saved": true, "id": value.ID, "format": value.Format, "state": value.State})
		}
		return jsonManagement(200, map[string]any{"saved": saved, "failed": len(results) - saved, "results": results}), nil
	case req.Method == "POST" && route == "account":
		var body struct {
			ID     string `json:"id"`
			Action string `json:"action"`
		}
		if json.Unmarshal(req.Body, &body) != nil || body.ID == "" {
			return jsonManagement(400, map[string]string{"error": "account id is required"}), nil
		}
		var err error
		r.accountMu.Lock()
		defer r.accountMu.Unlock()
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
		views = append(views, map[string]any{"id": a.AccountID, "state": a.State, "enabled": a.Enabled, "tier": a.Tier, "models": a.Models, "pending": a.Pending, "cooldown_until": a.CooldownUntil, "cooldown_reason": a.CooldownReason, "model_cooldowns": a.ModelCooldowns, "last_used": a.LastUsed, "hint": sessionHint(a.LastError)})
	}
	sort.Slice(views, func(i, j int) bool { return views[i]["id"].(string) < views[j]["id"].(string) })
	out := map[string]any{"enabled": true, "model_prefix": r.cfg.ModelPrefix, "version": version, "routing_strategy": r.cfg.RoutingStrategy, "per_account_concurrency": r.cfg.PerAccountConcurrency, "cooling_down": s.CoolingDown, "disabled": s.Disabled, "total": s.Total, "available": s.Available, "accounts": views}
	out["identity_policy_applications"] = identityApplied.Load()
	if s.Guest != nil {
		out["guest"] = map[string]any{"state": s.Guest.State, "models": s.Guest.Models}
	}
	return out
}
