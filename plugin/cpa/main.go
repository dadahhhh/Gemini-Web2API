package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

static const cliproxy_host_api* stored_host;

static void store_host_api(const cliproxy_host_api* host) {
	stored_host = host;
}

static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (stored_host == NULL || stored_host->call == NULL) {
		return 1;
	}
	return stored_host->call(stored_host->host_ctx, method, request, request_len, response);
}

static void free_host_buffer(void* ptr, size_t len) {
	if (stored_host != NULL && stored_host->free_buffer != NULL && ptr != NULL) {
		stored_host->free_buffer(ptr, len);
	}
}
*/
import "C"

import (
	"encoding/json"
	"errors"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"unsafe"
)

var engine = &Engine{host: callHost}

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil || (host != nil && uint32(host.abi_version) != 1) {
		return 1
	}
	C.store_host_api(host)
	plugin.abi_version = 1
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) (status C.int) {
	if response == nil {
		return 1
	}
	response.ptr = nil
	response.len = 0
	defer func() {
		if recover() != nil {
			writeEnvelope(response, nil, pluginabi.NewError("plugin_panic", "plugin operation failed", 500))
			status = 1
		}
	}()
	if method == nil || requestLen > 64<<20 || (request == nil && requestLen != 0) {
		writeEnvelope(response, nil, pluginabi.NewError("invalid_request", "invalid ABI request", 400))
		return 1
	}
	var raw []byte
	if requestLen > 0 {
		raw = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	result, err := engine.Handle(C.GoString(method), raw)
	writeEnvelope(response, result, err)
	if err != nil {
		return 1
	}
	return 0
}

func writeEnvelope(response *C.cliproxy_buffer, result any, err error) {
	envelope := map[string]any{"ok": err == nil}
	if err == nil {
		envelope["result"] = result
	} else {
		var typed *pluginabi.Error
		if !errors.As(err, &typed) {
			typed = pluginabi.NewError("plugin_error", "plugin operation failed; check configuration and account status", 500)
		}
		envelope["error"] = typed
	}
	raw, _ := json.Marshal(envelope)
	response.ptr = C.CBytes(raw)
	response.len = C.size_t(len(raw))
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, length C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() { engine.Close() }

func callHost(method string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	name := C.CString(method)
	defer C.free(unsafe.Pointer(name))
	body := C.CBytes(raw)
	defer C.free(body)
	var result C.cliproxy_buffer
	code := C.call_host_api(name, (*C.uint8_t)(body), C.size_t(len(raw)), &result)
	if result.ptr != nil {
		defer C.free_host_buffer(result.ptr, result.len)
	}
	if code != 0 {
		return errors.New("host callback rejected or request canceled")
	}
	if result.ptr != nil {
		var reply struct{ OK bool }
		if json.Unmarshal(C.GoBytes(result.ptr, C.int(result.len)), &reply) != nil || !reply.OK {
			return errors.New("host callback failed")
		}
	}
	return nil
}
