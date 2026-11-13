package main

import (
	"context"
	"encoding/json"
	"github.com/Mag1cFall/Gemini-Web2API/internal/gemini"
	"github.com/Mag1cFall/Gemini-Web2API/internal/requestdb"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"net/http"
	"testing"
	"time"
)

func TestIdentityPolicyPreservesRequestAccounting(t *testing.T) {
	for _, policy := range []bool{false, true} {
		e := newTestEngine(t)
		headers := http.Header{}
		if policy {
			encoded, _ := json.Marshal(map[string]any{"version": 1, "headers": gemini.BrowserIdentityHeaders("157.0.8103.40", time.Now())})
			headers.Set(browserIdentityHeader, string(encoded))
		}
		_, err := e.runtime.execute(pluginapi.ExecutorRequest{Model: "gemini-web/gemini-4-hydrogen", Format: "openai", Headers: headers, Payload: []byte(`{"messages":[{"role":"user","content":"synthetic accounting check"}]}`)}, false)
		if err == nil {
			t.Fatal("expected no-account error")
		}
		page, err := e.runtime.store.Records(context.Background(), requestdb.UsageRecordQuery{From: time.Now().Add(-time.Hour), To: time.Now().Add(time.Hour), Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("policy=%v: expected one request record, got %d", policy, len(page.Items))
		}
		e.Close()
	}
}
