package mcp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

const initializeRequestBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`

// postWithExternalHost gửi initialize qua listener loopback thật với Host công khai,
// đúng như reverse proxy cùng máy giữ nguyên Host.
func postWithExternalHost(t *testing.T, handler http.Handler) int {
	t.Helper()
	ts := httptest.NewServer(handler)
	defer ts.Close()

	req, err := http.NewRequest(http.MethodPost, ts.URL, strings.NewReader(initializeRequestBody))
	if err != nil {
		t.Fatalf("tạo request: %v", err)
	}
	req.Host = "gw.example.com"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("gửi request: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// CRUD server mở ra ngoài và được bảo vệ bằng bearer token, nên phải dùng được
// qua reverse proxy cùng máy giữ nguyên Host.
func TestCRUDServer_AcceptsExternalHostOnLoopback(t *testing.T) {
	status := postWithExternalHost(t, NewCRUDServer(CRUDDeps{}, "test"))
	if status != http.StatusOK {
		t.Fatalf("status = %d, muốn %d", status, http.StatusOK)
	}
}

// Bridge chỉ dùng cục bộ qua 127.0.0.1 nên giữ nguyên lớp chặn DNS rebinding.
func TestBridgeServer_RejectsExternalHostOnLoopback(t *testing.T) {
	status := postWithExternalHost(t, NewBridgeServer(tools.NewRegistry(), "test", nil, nil))
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, muốn %d", status, http.StatusForbidden)
	}
}
