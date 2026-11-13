package main

import (
	"encoding/json"
	"strings"
	"testing"
)

const fakeCookieArray = `[{"domain":".google.com","name":"SAPISID","value":"fake-sapi","expirationDate":1900000000},{"domain":".google.com","name":"__Secure-1PSID","value":"fake-psid","secure":true}]`

func importBody(field string, material any) []byte {
	raw, _ := json.Marshal(map[string]any{field: material})
	return raw
}

func TestImportFormatsGenerateStableID(t *testing.T) {
	var cookies any
	json.Unmarshal([]byte(fakeCookieArray), &cookies)
	cases := [][]byte{importBody("cookie", "SAPISID=fake-sapi; __Secure-1PSID=fake-psid"), importBody("material", fakeCookieArray), importBody("storage_state", cookies), importBody("storage_state", map[string]any{"cookies": cookies, "origins": []any{}})}
	first := ""
	for _, raw := range cases {
		state, _, err := normalizeImport(raw)
		if err != nil {
			t.Fatal(err)
		}
		if len(state.Cookies) != 2 || !strings.HasPrefix(state.Metadata.ID, "web-") {
			t.Fatal("normalization failed")
		}
		if first == "" {
			first = state.Metadata.ID
		}
		if first != state.Metadata.ID {
			t.Fatal("same session got different ID")
		}
	}
}

func TestImportNativeMetadataAndExpiration(t *testing.T) {
	var cookies any
	json.Unmarshal([]byte(fakeCookieArray), &cookies)
	body := importBody("storage_state", map[string]any{"cookies": cookies, "geminiWeb2api": map[string]any{"id": "my-pro", "language": "en-US", "proxy": "direct"}})
	state, _, err := normalizeImport(body)
	if err != nil {
		t.Fatal(err)
	}
	if state.Metadata.ID != "my-pro" || state.Metadata.Proxy != "direct" || state.Metadata.Language != "en-US" {
		t.Fatal("native metadata lost")
	}
	if state.Cookies[0].Expires != 1900000000 {
		t.Fatal("export expirationDate lost")
	}
}

func TestImportMarkdownCookieNamesPreserveValues(t *testing.T) {
	state, _, err := normalizeImport(importBody("cookie", `SAPISID=fake\_value; \_\_Secure-1PSID=fake-psid`))
	if err != nil {
		t.Fatal(err)
	}
	if state.Cookies[0].Value != `fake\_value` || state.Cookies[1].Name != "__Secure-1PSID" {
		t.Fatal("cookie value was changed")
	}
}

func TestImportFailuresAreActionableAndDoNotEchoMaterial(t *testing.T) {
	for _, raw := range [][]byte{importBody("cookie", "broken-private-material"), importBody("material", `[{"value":"private`), importBody("storage_state", map[string]any{"cookies": []any{}})} {
		_, _, err := normalizeImport(raw)
		if err == nil {
			t.Fatal("malformed import accepted")
		}
		encoded, _ := json.Marshal(publicImportError(err))
		if strings.Contains(string(encoded), "private") {
			t.Fatal("material leaked")
		}
	}
}
