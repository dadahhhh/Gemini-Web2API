package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
		identity := ""
		for _, c := range cookies {
			if c.Name == "__Secure-1PSID" {
				identity = c.Value
				break
			}
		}
		sum := sha256.Sum256([]byte(identity))
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

func publicImportError(err error) any {
	var typed *importFailure
	if errors.As(err, &typed) {
		return typed
	}
	return &importFailure{"import_failed", "账号保存失败，请检查持久目录权限"}
}

func sessionHint(raw string) string {
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
