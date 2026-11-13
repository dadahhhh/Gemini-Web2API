package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mag1cFall/Gemini-Web2API/internal/auth"
	"github.com/Mag1cFall/Gemini-Web2API/internal/balancer"
	"github.com/Mag1cFall/Gemini-Web2API/internal/gemini"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func disabledMaterial(t *testing.T, id, psid string) string {
	t.Helper()
	var cookies []auth.StateCookie
	if err := json.Unmarshal([]byte(fakeCookieArray), &cookies); err != nil {
		t.Fatal(err)
	}
	cookies[1].Value = psid
	raw, _ := json.Marshal(auth.StorageState{Cookies: cookies, Metadata: auth.Metadata{ID: id, Disabled: true}})
	return string(raw)
}
func callManagement(t *testing.T, e *Engine, path string, body any) pluginapi.ManagementResponse {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := json.Marshal(pluginapi.ManagementRequest{Method: "POST", Path: "/v0/management/plugins/" + pluginID + "/" + path, Body: raw})
	value, err := e.Handle("management.handle", req)
	if err != nil {
		t.Fatal(err)
	}
	return value.(pluginapi.ManagementResponse)
}
func TestBatchAccountsPartialFailureAndPersistence(t *testing.T) {
	e := newTestEngine(t)
	a := map[string]any{"material": disabledMaterial(t, "first", "fake-first")}
	b := map[string]any{"material": disabledMaterial(t, "second", "fake-second")}
	res := callManagement(t, e, "import-batch", map[string]any{"accounts": []any{a, b, map[string]any{"cookie": "private-invalid-cookie"}, a}})
	var response struct {
		Saved  int `json:"saved"`
		Failed int `json:"failed"`
	}
	json.Unmarshal(res.Body, &response)
	if res.StatusCode != 200 || response.Saved != 2 || response.Failed != 2 {
		t.Fatalf("batch: %s", res.Body)
	}
	if strings.Contains(string(res.Body), "fake-first") || strings.Contains(string(res.Body), "private-invalid-cookie") {
		t.Fatal("credential leak")
	}
	state, _ := json.Marshal(e.runtime.statusView())
	if !strings.Contains(string(state), `"total":2`) || !strings.Contains(string(state), `"disabled":2`) {
		t.Fatalf("status: %s", state)
	}
	cfg := e.cfg
	e.Close()
	next := &Engine{}
	defer next.Close()
	raw, _ := json.Marshal(map[string]any{"config_yaml": []byte("data_dir: " + cfg.DataDir + "\nguest_enabled: false\n")})
	if _, err := next.Handle("plugin.register", raw); err != nil {
		t.Fatal(err)
	}
	<-next.runtime.initialized
	if next.runtime.pool.Status().Total != 2 || next.runtime.pool.Status().Disabled != 2 {
		t.Fatal("accounts not restored")
	}
}
func TestCreateProtectsExistingAndDuplicateSession(t *testing.T) {
	e := newTestEngine(t)
	a := map[string]any{"material": disabledMaterial(t, "first", "fake-first"), "mode": "create"}
	if callManagement(t, e, "import", a).StatusCode != 200 {
		t.Fatal("initial import failed")
	}
	path := filepath.Join(e.cfg.DataDir, "auth", "first", "storage-state.json")
	before, _ := os.ReadFile(path)
	a["material"] = disabledMaterial(t, "first", "fake-replacement")
	res := callManagement(t, e, "import", a)
	if res.StatusCode != 400 || !strings.Contains(string(res.Body), "account_exists") {
		t.Fatal("existing account overwritten")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("existing file changed")
	}
	res = callManagement(t, e, "import", map[string]any{"material": disabledMaterial(t, "alias", "fake-first"), "mode": "create"})
	if res.StatusCode != 400 || !strings.Contains(string(res.Body), "duplicate_session") {
		t.Fatal("duplicate session added")
	}
	res = callManagement(t, e, "import", map[string]any{"material": disabledMaterial(t, "first", "fake-replacement"), "mode": "update"})
	if res.StatusCode != 200 {
		t.Fatal("explicit update rejected")
	}
	if e.runtime.pool.Status().Total != 1 || e.runtime.pool.Status().Disabled != 1 {
		t.Fatal("update duplicated or enabled account")
	}
}
func TestGoogleAccountIndexSeparatesSharedBrowser(t *testing.T) {
	zero, _, err := normalizeImport(importBody("material", fakeCookieArray))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"material": fakeCookieArray, "auth_user": 1})
	one, _, err := normalizeImport(raw)
	if err != nil || zero.Metadata.ID == one.Metadata.ID || one.Metadata.AuthUser != 1 {
		t.Fatal("Google account indices collided")
	}
	raw, _ = json.Marshal(map[string]any{"material": fakeCookieArray, "auth_user": -1})
	if _, _, err := normalizeImport(raw); err == nil {
		t.Fatal("invalid index accepted")
	}
}
func TestBatchBoundsAndUnknownUpdate(t *testing.T) {
	e := newTestEngine(t)
	items := make([]any, 11)
	if callManagement(t, e, "import-batch", map[string]any{"accounts": items}).StatusCode != 400 {
		t.Fatal("unbounded batch")
	}
	res := callManagement(t, e, "import", map[string]any{"material": disabledMaterial(t, "missing", "fake-missing"), "mode": "update"})
	if res.StatusCode != 400 || !strings.Contains(string(res.Body), "account_missing") {
		t.Fatal("missing update created account")
	}
}

func TestMultiAccountRotationSkipsCoolingAndDisabled(t *testing.T) {
	pool := balancer.NewAccountPool(time.Minute, time.Hour)
	pool.SetStrategy(balancer.RoundRobin)
	for _, id := range []string{"first", "second"} {
		client, err := gemini.NewClient(gemini.AccountSource{ID: id, Cookies: []gemini.Cookie{{Name: "SAPISID", Value: "fake-sapi"}, {Name: "__Secure-1PSID", Value: "fake-psid"}}}, "", false)
		if err != nil {
			t.Fatal(err)
		}
		pool.Register(id, balancer.AccountInfo{}, false)
		pool.SetClient(id, client)
		pool.MarkReady(id)
	}
	_, first := pool.NextForModelExcluding("", "", nil)
	_, second := pool.NextForModelExcluding("", "", nil)
	if first == "" || second == "" || first == second {
		t.Fatal("round robin did not distribute accounts")
	}
	pool.ReportFailure(first, "", &gemini.ProtocolError{HTTPStatus: 403, Code: 1060, Message: "test IP restriction"})
	if _, got := pool.NextForModelExcluding("", "", nil); got != second {
		t.Fatal("cooling account was selected")
	}
	pool.SetDisabled(second, true)
	if client, _ := pool.NextForModelExcluding("", "", nil); client != nil {
		t.Fatal("unavailable accounts were selected")
	}
}
