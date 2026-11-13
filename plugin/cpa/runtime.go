package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/Mag1cFall/Gemini-Web2API/internal/accounts"
	"github.com/Mag1cFall/Gemini-Web2API/internal/adapter"
	"github.com/Mag1cFall/Gemini-Web2API/internal/auth"
	"github.com/Mag1cFall/Gemini-Web2API/internal/balancer"
	"github.com/Mag1cFall/Gemini-Web2API/internal/requestdb"
	"github.com/gin-gonic/gin"
	"github.com/gofrs/flock"
)

// Runtime hosts the upstream adapters in process; it opens no listening socket.
type Runtime struct {
	cfg         Config
	handler     http.Handler
	pool        *balancer.AccountPool
	manager     *accounts.Manager
	store       *requestdb.Store
	lock        *flock.Flock
	ctx         context.Context
	cancel      context.CancelFunc
	initialized chan struct{}
	mu          sync.Mutex
	closed      bool
	active      sync.WaitGroup
}

func newRuntime(c Config) (*Runtime, error) {
	if err := os.MkdirAll(c.DataDir, 0700); err != nil {
		return nil, err
	}
	lock := flock.New(filepath.Join(c.DataDir, "plugin.lock"))
	ok, err := lock.TryLock()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("data_dir is already in use by another plugin instance")
	}
	store, err := requestdb.Open(filepath.Join(c.DataDir, "requests.db"))
	if err != nil {
		lock.Unlock()
		return nil, err
	}
	authdir := filepath.Join(c.DataDir, "auth")
	if err = os.MkdirAll(authdir, 0700); err != nil {
		store.Close()
		lock.Unlock()
		return nil, err
	}
	loaded, err := auth.LoadFiles([]string{authdir})
	if err != nil {
		store.Close()
		lock.Unlock()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	pool := balancer.NewAccountPool(duration(c.AccountCooldown), duration(c.SessionTTL))
	pool.SetStrategy(c.RoutingStrategy)
	manager := accounts.New(ctx, pool, accounts.Settings{InitTimeout: duration(c.InitTimeout), RetryInterval: duration(c.AccountCooldown), Proxy: c.Proxy, AuthDir: authdir, Concurrency: c.PerAccountConcurrency})
	adapter.ConfigureSessionTTL(duration(c.SessionTTL))
	adapter.SetResponseModes(false, false)
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Recovery(), adapter.RequestMiddleware(store))
	// Caller authentication belongs to CPA. No client authorization headers are forwarded here.
	router.POST("/v1/chat/completions", adapter.ChatCompletionHandler(pool))
	router.POST("/v1/responses", adapter.ResponsesHandler(pool, store))
	router.POST("/v1/messages", adapter.ClaudeMessagesHandler(pool))
	router.POST("/v1/messages/count_tokens", adapter.ClaudeCountTokensHandler(pool))
	router.POST("/v1beta/models/*action", adapter.GeminiRouterHandler(pool, store))
	router.GET("/v1/models", adapter.ListModelsHandler(pool))
	r := &Runtime{cfg: c, handler: router, pool: pool, manager: manager, store: store, lock: lock, ctx: ctx, cancel: cancel, initialized: make(chan struct{})}
	go func() { defer close(r.initialized); manager.StartWithGuest(loaded, c.GuestEnabled) }()
	return r, nil
}

func (r *Runtime) acquire() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return false
	}
	r.active.Add(1)
	return true
}
func (r *Runtime) Close() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	r.cancel()
	r.mu.Unlock()
	r.active.Wait()
	<-r.initialized
	r.manager.Wait()
	r.store.Close()
	r.lock.Unlock()
}
