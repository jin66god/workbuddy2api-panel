// pprof_test.go 诊断监听的路由注册：main.go 用 `_ "net/http/pprof"` 把 handler 挂到
// DefaultServeMux，再由 WB2API_PPROF 起独立监听。少这行 import 时监听能起来但
// /debug/pprof/* 全是 404（静默失效），所以这里直接钉住注册结果。
package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPprofHandlersRegistered(t *testing.T) {
	cases := []string{"/debug/pprof/", "/debug/pprof/cmdline", "/debug/pprof/goroutine?debug=1"}
	for _, path := range cases {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		http.DefaultServeMux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s 未注册（status=%d）：main.go 少了 _ \"net/http/pprof\" 导入", path, rec.Code)
		}
	}
}
