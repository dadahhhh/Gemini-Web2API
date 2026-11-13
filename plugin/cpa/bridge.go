package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Mag1cFall/Gemini-Web2API/internal/gemini"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const maxResponseBytes = 64 << 20

const browserIdentityHeader = "X-Cpa-Web-Gemini-Identity"

var identityApplied atomic.Uint64

func browserIdentityContext(ctx context.Context, headers http.Header) context.Context {
	raw := headers.Get(browserIdentityHeader)
	if raw == "" || len(raw) > 16<<10 {
		return ctx
	}
	var profile struct {
		Version int               `json:"version"`
		Headers map[string]string `json:"headers"`
	}
	if json.Unmarshal([]byte(raw), &profile) != nil || profile.Version != 1 {
		return ctx
	}
	identityApplied.Add(1)
	return gemini.WithBrowserIdentity(ctx, profile.Headers)
}

func requestFor(c Config, in pluginapi.ExecutorRequest, count bool) (*http.Request, error) {
	model, ok := strings.CutPrefix(in.Model, c.ModelPrefix)
	if !ok || model == "" {
		return nil, pluginabi.NewError("invalid_model", "use the configured Gemini Web model prefix", 400)
	}
	format := in.Format
	if format == "" {
		format = in.SourceFormat
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(in.Payload, &body); err != nil || body == nil {
		return nil, pluginabi.NewError("invalid_request", "payload must be a JSON object", 400)
	}
	body["model"], _ = json.Marshal(model)
	body["stream"], _ = json.Marshal(in.Stream)
	path := ""
	switch format {
	case "chat-completions", "openai":
		if count {
			return nil, pluginabi.NewError("not_implemented", "count_tokens requires anthropic or gemini format", 501)
		}
		path = "/v1/chat/completions"
	case "responses", "openai-response":
		if count {
			return nil, pluginabi.NewError("not_implemented", "count_tokens requires anthropic or gemini format", 501)
		}
		path = "/v1/responses"
	case "anthropic", "claude":
		path = "/v1/messages"
		if count {
			path += "/count_tokens"
		}
	case "gemini":
		action := "generateContent"
		if in.Stream {
			action = "streamGenerateContent"
		}
		if count {
			action = "countTokens"
		}
		path = "/v1beta/models/" + url.PathEscape(model) + ":" + action
	default:
		return nil, pluginabi.NewError("unsupported_format", "unsupported input protocol", 400)
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, "http://embedded.invalid"+path, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:0"
	// Only conversation IDs are useful to the upstream adapters. Never copy CPA keys or cookies.
	for _, name := range []string{"X-Session-ID", "X-Conversation-ID"} {
		if value := in.Headers.Get(name); value != "" {
			req.Header.Set(name, value)
		}
	}
	return req, nil
}

// pipeWriter preserves arbitrary SSE byte boundaries, with backpressure and cancellation.
type pipeWriter struct {
	header http.Header
	writer *io.PipeWriter
	ready  chan struct{}
	once   sync.Once
	status int
	frozen http.Header
}

func (w *pipeWriter) Header() http.Header { return w.header }
func (w *pipeWriter) WriteHeader(status int) {
	w.once.Do(func() { w.status = status; w.frozen = w.header.Clone(); close(w.ready) })
}
func (w *pipeWriter) Write(b []byte) (int, error) { w.WriteHeader(200); return w.writer.Write(b) }
func (w *pipeWriter) Flush()                      { w.WriteHeader(200) }

type HTTPResult struct {
	Status int
	Header http.Header
	Body   io.ReadCloser
}

func servePipe(ctx context.Context, h http.Handler, req *http.Request) (HTTPResult, error) {
	reader, writer := io.Pipe()
	w := &pipeWriter{header: make(http.Header), writer: writer, ready: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			reader.CloseWithError(ctx.Err())
			writer.CloseWithError(ctx.Err())
		case <-done:
		}
	}()
	go func() {
		defer close(done)
		defer writer.Close()
		defer w.WriteHeader(200)
		defer func() {
			if recover() != nil {
				writer.CloseWithError(errors.New("embedded handler failed"))
			}
		}()
		h.ServeHTTP(w, req.WithContext(ctx))
	}()
	select {
	case <-ctx.Done():
		reader.Close()
		return HTTPResult{}, ctx.Err()
	case <-w.ready:
		return HTTPResult{w.status, w.frozen, reader}, nil
	}
}

func readBounded(body io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(body, maxResponseBytes+1))
	if err == nil && len(b) > maxResponseBytes {
		return nil, errors.New("response exceeds 64 MiB")
	}
	return b, err
}
func responseError(status int, body []byte) error {
	// Do not echo upstream payloads: they can contain account or session material.
	return pluginabi.NewError("upstream_error", fmt.Sprintf("Gemini Web returned HTTP %d; inspect account status", status), status)
}

func (r *Runtime) execute(in pluginapi.ExecutorRequest, count bool) (pluginapi.ExecutorResponse, error) {
	req, err := requestFor(r.cfg, in, count)
	if err != nil {
		return pluginapi.ExecutorResponse{}, err
	}
	ctx, cancel := context.WithTimeout(r.ctx, duration(r.cfg.RequestTimeout))
	defer cancel()
	ctx = browserIdentityContext(ctx, in.Headers)
	result, err := servePipe(ctx, r.handler, req)
	if err != nil {
		return pluginapi.ExecutorResponse{}, err
	}
	defer result.Body.Close()
	body, err := readBounded(result.Body)
	if err != nil {
		return pluginapi.ExecutorResponse{}, err
	}
	if result.Status >= 400 {
		return pluginapi.ExecutorResponse{}, responseError(result.Status, body)
	}
	return pluginapi.ExecutorResponse{Payload: body, Headers: result.Header}, nil
}

func (r *Runtime) stream(in pluginapi.ExecutorRequest, emit func([]byte) error) error {
	in.Stream = true
	req, err := requestFor(r.cfg, in, false)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(r.ctx, duration(r.cfg.RequestTimeout))
	defer cancel()
	ctx = browserIdentityContext(ctx, in.Headers)
	result, err := servePipe(ctx, r.handler, req)
	if err != nil {
		return err
	}
	defer result.Body.Close()
	if result.Status >= 400 {
		return responseError(result.Status, nil)
	}
	buf := make([]byte, 16<<10)
	for {
		n, readErr := result.Body.Read(buf)
		if n > 0 {
			if err := emit(append([]byte(nil), buf[:n]...)); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func (r *Runtime) models() []pluginapi.ModelInfo {
	recorder := httptest.NewRecorder()
	r.handler.ServeHTTP(recorder, httptest.NewRequest("GET", "/v1/models", nil))
	var catalog struct {
		Data []struct {
			ID          string `json:"id"`
			Description string `json:"description"`
		} `json:"data"`
	}
	_ = json.Unmarshal(recorder.Body.Bytes(), &catalog)
	ids := map[string]string{}
	for _, m := range catalog.Data {
		ids[m.ID] = m.Description
	}
	// Bootstrap entries allow explicit routing before account discovery finishes.
	if len(ids) == 0 {
		for _, id := range []string{"gemini-4-hydrogen", "gemini-3.5-flash-lite", "gemini-4-carbon", "gemini-4-argon"} {
			ids[id] = "Account availability is checked by Gemini Web"
		}
	}
	out := make([]pluginapi.ModelInfo, 0, len(ids))
	for id, desc := range ids {
		out = append(out, pluginapi.ModelInfo{ID: r.cfg.ModelPrefix + id, Object: "model", OwnedBy: pluginID, Type: pluginID, Description: desc})
	}
	return out
}
