package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Mag1cFall/Gemini-Web2API/internal/auth"
	"github.com/Mag1cFall/Gemini-Web2API/internal/chromeauth"
)

type importFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *importFailure) Error() string         { return e.Message }
func invalidImport(code, message string) error { return &importFailure{code, message} }

// normalizeImport accepts the old fields and a new format-neutral material field.
// All cookies pass through the upstream validator; no parser error is echoed verbatim.
func normalizeImport(body []byte) (auth.StorageState, string, error) {
	var request struct {
		Material string          `json:"material"`
		Cookie   string          `json:"cookie"`
		State    json.RawMessage `json:"storage_state"`
		ID       string          `json:"id"`
		AuthUser *int            `json:"auth_user"`
	}
	var state auth.StorageState
	if len(body) > 4<<20 {
		return state, "", invalidImport("input_too_large", "导入内容超过 4 MiB")
	}
	if json.Unmarshal(body, &request) != nil {
		return state, "", invalidImport("invalid_request", "导入请求格式无效")
	}
	material := request.Material
	if material == "" {
		material = request.Cookie
	}
	if material == "" && len(request.State) > 0 && string(request.State) != "null" {
		material = string(request.State)
		if strings.HasPrefix(strings.TrimSpace(material), `"`) {
			if json.Unmarshal(request.State, &material) != nil {
				return state, "", invalidImport("invalid_json", "JSON 格式无效")
			}
		}
	}
	material = strings.TrimSpace(strings.TrimPrefix(material, "\uFEFF"))
	if strings.HasPrefix(material, "```") && strings.HasSuffix(material, "```") {
		if line := strings.IndexByte(material, '\n'); line >= 0 {
			material = strings.TrimSpace(material[line+1 : len(material)-3])
		}
	}
	if material == "" {
		return state, "", invalidImport("empty_material", "请粘贴 Cookie、Cookie JSON 或 StorageState")
	}
	format := "cookie-header"
	if material[0] == '[' || material[0] == '{' {
		if !json.Valid([]byte(material)) {
			return state, "", invalidImport("invalid_json", "JSON 格式无效，请检查是否复制完整")
		}
		format = "cookie-json"
		if material[0] == '{' {
			if json.Unmarshal([]byte(material), &state) != nil {
				return state, "", invalidImport("invalid_storage_state", "StorageState 对象格式无效")
			}
			format = "storage-state"
		}
	} else if strings.Contains(material, "\t") {
		format = "netscape"
	}
	cookies, err := chromeauth.ParseCookieMaterial(material)
	if err != nil {
		// Only fixed messages and cookie names may cross the public response boundary.
		if strings.Contains(err.Error(), "缺少核心 Cookie") {
			return state, "", invalidImport("missing_required_cookies", "缺少 SAPISID 或 __Secure-1PSID，请从已登录 Gemini 的浏览器完整导出")
		}
		return state, "", invalidImport("invalid_cookie_material", "Cookie 格式无效，支持 Cookie Header、Cookie 数组和 StorageState")
	}
	state.Cookies = cookies
	if request.AuthUser != nil {
		state.Metadata.AuthUser = *request.AuthUser
	}
	if state.Metadata.AuthUser < 0 || state.Metadata.AuthUser > 99 {
		return state, "", invalidImport("invalid_auth_user", "Google 账号序号需在 0 到 99 之间")
	}
	chosen := request.ID
	if chosen == "" {
		chosen = state.Metadata.ID
	}
	if strings.TrimSpace(chosen) != "" {
		state.Metadata.ID = chromeauth.AccountID(chosen)
		if state.Metadata.ID == "" {
			return state, "", invalidImport("invalid_account_id", "账号名称需包含英文字母或数字，也可以留空自动生成")
		}
	} else {
		sum := sha256.Sum256([]byte(sessionIdentity(state)))
		state.Metadata.ID = "web-" + hex.EncodeToString(sum[:6])
	}
	if state.Metadata.Source.Browser == "" {
		state.Metadata.Source.Browser = "cookie-import"
	}
	if state.Metadata.Language == "" {
		state.Metadata.Language = "zh-CN"
	}
	if state.Origins == nil {
		state.Origins = []json.RawMessage{}
	}
	return state, format, nil
}

// Include the Google account index, so one browser session can hold distinct accounts.
func sessionIdentity(state auth.StorageState) string {
	for _, c := range state.Cookies {
		if c.Name == "__Secure-1PSID" {
			if state.Metadata.AuthUser == 0 {
				return c.Value
			}
			return fmt.Sprintf("%s|authuser=%d", c.Value, state.Metadata.AuthUser)
		}
	}
	return ""
}

type savedImport struct {
	ID      string `json:"id"`
	Format  string `json:"format"`
	State   string `json:"state"`
	Message string `json:"message"`
}

// Caller holds accountMu across duplicate checks and persistence.
func (r *Runtime) importOne(body []byte) (savedImport, error) {
	var result savedImport
	state, format, err := normalizeImport(body)
	if err != nil {
		return result, err
	}
	var options struct {
		Mode     string `json:"mode"`
		AuthUser *int   `json:"auth_user"`
	}
	_ = json.Unmarshal(body, &options)
	if options.Mode == "" {
		options.Mode = "upsert"
	} // Preserve the v0.1 management API.
	if options.Mode != "create" && options.Mode != "update" && options.Mode != "upsert" {
		return result, invalidImport("invalid_mode", "请选择新增账号或更新已有账号")
	}
	authdir := filepath.Join(r.cfg.DataDir, "auth")
	target := filepath.Join(authdir, state.Metadata.ID, "storage-state.json")
	_, statErr := os.Stat(target)
	exists := statErr == nil
	if statErr != nil && !os.IsNotExist(statErr) {
		return result, statErr
	}
	if options.Mode == "create" && exists {
		return result, invalidImport("account_exists", "账号已存在，请在列表中选择更新凭证")
	}
	if options.Mode == "update" && !exists {
		return result, invalidImport("account_missing", "待更新账号不存在，请刷新账号列表")
	}
	if exists && options.AuthUser == nil && state.Metadata.AuthUser == 0 {
		raw, err := os.ReadFile(target)
		if err != nil {
			return result, err
		}
		var old auth.StorageState
		if json.Unmarshal(raw, &old) != nil {
			return result, errors.New("invalid stored account")
		}
		state.Metadata.AuthUser = old.Metadata.AuthUser
	}
	paths, err := filepath.Glob(filepath.Join(authdir, "*", "storage-state.json"))
	if err != nil {
		return result, err
	}
	for _, p := range paths {
		if p == target {
			continue
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return result, err
		}
		var other auth.StorageState
		if json.Unmarshal(raw, &other) != nil {
			return result, errors.New("invalid stored account")
		}
		if sessionIdentity(other) == sessionIdentity(state) {
			return result, invalidImport("duplicate_session", "这份 Google 会话已导入，请更新已有账号，不要重复添加")
		}
	}
	encoded, _ := json.Marshal(state)
	id, err := r.manager.ImportState(encoded)
	if err != nil {
		return result, err
	}
	return savedImport{ID: id, Format: format, State: "initializing", Message: "账号已保存，正在验证 Google 会话，请刷新状态"}, nil
}

func publicImportError(err error) any {
	var typed *importFailure
	if errors.As(err, &typed) {
		return typed
	}
	return &importFailure{"import_failed", "账号保存失败，请检查持久目录权限"}
}

func sessionHint(raw string) string {
	if strings.Contains(raw, "出口 IP") {
		return "Google 暂时限制当前出口 IP，请等待恢复或检查网页出口"
	}
	for code, message := range map[string]string{"(1040)": "需要在 Gemini 官网同意最新服务条款", "(1042)": "服务条款已更新，请在 Gemini 官网重新同意", "(1054)": "账号访问被家长或监护人阻止", "(1057)": "账号访问需要家长或监护人批准", "(1060)": "当前出口地区不支持 Gemini", "(1014)": "Google 暂时限制该账号访问", "(1021)": "Google 拒绝了该账号的访问"} {
		if strings.Contains(raw, code) {
			return message
		}
	}
	switch {
	case raw == "":
		return ""
	case strings.Contains(raw, "1016") || strings.Contains(raw, "失效") || strings.Contains(raw, "signed out"):
		return "Google 会话已失效，请重新导出 Cookie"
	case strings.Contains(raw, "人机") || strings.Contains(raw, "captcha"):
		return "Google 要求人机验证，请先在浏览器完成验证"
	case strings.Contains(raw, "地区") || strings.Contains(raw, "账号不可用"):
		return "账号受地区、条款或访问权限限制"
	case strings.Contains(raw, "EOF") || strings.Contains(raw, "timeout") || strings.Contains(raw, "超时") || strings.Contains(raw, "connect"):
		return "连接 Google 失败，正在重试，请检查出口网络"
	default:
		return "账号初始化暂未通过，后台会继续重试"
	}
}
