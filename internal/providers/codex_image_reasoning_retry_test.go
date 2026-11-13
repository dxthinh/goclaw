package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const imageComplexityFailedEvent = `{"type":"response.failed","response":{"status":"failed","error":{"code":"image_generation_user_error","message":"Complexity 'high' is not supported for this image model."}}}`

// complexityServer giả lập backend: request có reasoning thì trả response.failed vì
// complexity, request không có reasoning thì trả lời bình thường. preFailure là các
// event phát ra trước khi fail.
func complexityServer(t *testing.T, preFailure ...string) (*httptest.Server, func() []map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		if _, hasReasoning := body["reasoning"]; hasReasoning {
			for _, ev := range preFailure {
				fmt.Fprintf(w, "data: %s\n\n", ev)
			}
			fmt.Fprintf(w, "data: %s\n\n", imageComplexityFailedEvent)
			return
		}
		fmt.Fprintf(w, "data: %s\n\n", `{"type":"response.output_text.delta","delta":"done"}`)
		fmt.Fprintf(w, "data: %s\n\n", `{"type":"response.completed","response":{"id":"r1","status":"completed"}}`)
	}))
	t.Cleanup(server.Close)
	return server, func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]any(nil), bodies...)
	}
}

func complexityTestProvider(url string) *CodexProvider {
	p := NewCodexProvider("openai-codex", &staticTokenSource{token: "tok"}, url, "gpt-test")
	p.retryConfig.Attempts = 1
	return p
}

func TestCodexChat_ImageComplexityRejection_RetriesWithoutReasoning(t *testing.T) {
	server, requests := complexityServer(t)
	p := complexityTestProvider(server.URL)

	opts := map[string]any{OptThinkingLevel: "high"}
	result, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "vẽ"}},
		Options:  opts,
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if result.Content != "done" {
		t.Errorf("Content = %q, muốn %q", result.Content, "done")
	}
	got := requests()
	if len(got) != 2 {
		t.Fatalf("số request = %d, muốn 2", len(got))
	}
	if _, ok := got[0]["reasoning"]; !ok {
		t.Error("request đầu phải có reasoning")
	}
	if _, ok := got[1]["reasoning"]; ok {
		t.Error("request gọi lại không được có reasoning")
	}
	if opts[OptThinkingLevel] != "high" {
		t.Error("Options của nơi gọi không được bị sửa")
	}
}

// Không có callback thì chữ mở đầu mới chỉ nằm trong result, chưa tới nơi gọi: vẫn gọi
// lại, và kết quả trả về chỉ gồm nội dung của lượt gọi lại.
func TestCodexChat_ImageComplexityRejection_RetriesWhenTextOnlyBuffered(t *testing.T) {
	server, requests := complexityServer(t, `{"type":"response.output_text.delta","delta":"Em vẽ đây"}`)
	p := complexityTestProvider(server.URL)

	result, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "vẽ"}},
		Options:  map[string]any{OptThinkingLevel: "high"},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if result.Content != "done" {
		t.Errorf("Content = %q, muốn %q (không lẫn chữ của lượt bị bỏ)", result.Content, "done")
	}
	if n := len(requests()); n != 2 {
		t.Errorf("số request = %d, muốn 2", n)
	}
}

// Backend vẫn từ chối dù không còn reasoning: chỉ gọi lại đúng một lần rồi trả lỗi.
func TestCodexChat_ImageComplexityRejection_RetriesOnlyOnce(t *testing.T) {
	var mu sync.Mutex
	count := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		count++
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n", imageComplexityFailedEvent)
	}))
	defer server.Close()
	p := complexityTestProvider(server.URL)

	_, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "vẽ"}},
		Options:  map[string]any{OptThinkingLevel: "medium"},
	})
	if err == nil {
		t.Fatal("muốn lỗi")
	}
	if count != 2 {
		t.Errorf("số request = %d, muốn 2", count)
	}
}

func TestIsImageComplexityRejection(t *testing.T) {
	cases := map[string]bool{
		"codex: response failed: Complexity 'high' is not supported for this image model.": true,
		"HTTP 400: complexity 'low' is not supported for this image model":                 true,
		"codex: response failed: An error occurred while processing your request":          false,
		"model does not support images":                                                    false,
	}
	for msg, want := range cases {
		if got := isImageComplexityRejection(fmt.Errorf("%s", msg)); got != want {
			t.Errorf("%q = %v, muốn %v", msg, got, want)
		}
	}
	if isImageComplexityRejection(nil) {
		t.Error("nil không phải lỗi complexity")
	}
}

// Chunk ảnh được phát ngay cho client nhưng chỉ vào result.Images khi stream kết thúc,
// nên phải dựa vào những gì đã phát ra chứ không dựa vào result.
func TestCodexChatStream_ImageComplexityRejection_NoReplayAfterImageChunk(t *testing.T) {
	server, requests := complexityServer(t,
		`{"type":"response.image_generation_call.partial_image","item_id":"ig_1","partial_image_b64":"aGVsbG8=","output_format":"png"}`)
	p := complexityTestProvider(server.URL)

	images := 0
	_, err := p.ChatStream(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "vẽ"}},
		Options:  map[string]any{OptThinkingLevel: "high"},
	}, func(c StreamChunk) { images += len(c.Images) })
	if images == 0 {
		t.Fatal("server giả lập phải phát ra chunk ảnh")
	}
	if err == nil || !strings.Contains(err.Error(), "Complexity") {
		t.Fatalf("err = %v, muốn lỗi Complexity", err)
	}
	if n := len(requests()); n != 1 {
		t.Errorf("số request = %d, muốn 1 vì ảnh đã phát ra", n)
	}
}

func TestCodexChatStream_ImageComplexityRejection_NoReplayAfterTextChunk(t *testing.T) {
	server, requests := complexityServer(t, `{"type":"response.output_text.delta","delta":"Em vẽ đây"}`)
	p := complexityTestProvider(server.URL)

	_, err := p.ChatStream(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "vẽ"}},
		Options:  map[string]any{OptThinkingLevel: "high"},
	}, func(StreamChunk) {})
	if err == nil {
		t.Fatal("muốn lỗi")
	}
	if n := len(requests()); n != 1 {
		t.Errorf("số request = %d, muốn 1 vì chữ đã phát ra", n)
	}
}
