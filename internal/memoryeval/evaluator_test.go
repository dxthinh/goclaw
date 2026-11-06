package memoryeval

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
)

const goodResponse = `{"relevant_items":2,"used":true,"reason":"phần lớn các mục đúng chủ đề"}`

func TestEvaluator_RecordsScoreAndNumbers(t *testing.T) {
	f := newEvalFixture(goodResponse)
	c := f.candidate()
	f.evaluator.process(context.Background(), c)

	if len(f.store.results) != 1 {
		t.Fatalf("results = %d, muốn 1", len(f.store.results))
	}
	r := f.store.results[0]
	if r.ItemCount != 3 || r.RelevantItems != 2 || !r.Used || !r.HasEpisodic || r.Channel != c.Channel {
		t.Fatalf("kết quả sai: %+v", r)
	}
	if r.Provider != "fake" || r.Model != "fake-model" || r.InputTokens != 11 || r.OutputTokens != 7 {
		t.Fatalf("thông tin provider sai: %+v", r)
	}
	if r.TraceID == nil || *r.TraceID != c.TraceID {
		t.Fatalf("trace_id = %v, muốn %v", r.TraceID, c.TraceID)
	}
	if !f.provider.dead[0] {
		t.Fatal("lời gọi provider phải có timeout")
	}
	if got := f.provider.reqs[0].Options[providers.OptMaxTokens]; got != maxOutputTokens {
		t.Fatalf("max_tokens = %v, muốn %d", got, maxOutputTokens)
	}
}

func TestEvaluator_DoesNotExceedDailyLimit(t *testing.T) {
	f := newEvalFixture(goodResponse)
	f.configs.values[configEvalDailyLimit] = "2"
	for range 5 {
		f.evaluator.process(context.Background(), f.candidate())
	}
	if f.provider.calls != 2 || len(f.store.results) != 2 {
		t.Fatalf("calls = %d, results = %d, muốn 2 và 2", f.provider.calls, len(f.store.results))
	}
}

func TestEvaluator_DefaultDailyLimit(t *testing.T) {
	f := newEvalFixture(goodResponse)
	for range defaultDailyLimit + 5 {
		f.evaluator.process(context.Background(), f.candidate())
	}
	if f.provider.calls != defaultDailyLimit {
		t.Fatalf("calls = %d, muốn %d", f.provider.calls, defaultDailyLimit)
	}
}

// Lượt thất bại không để lại dòng nào, nhưng vẫn phải tính vào hạn mức để provider trả phản
// hồi hỏng không tốn lời gọi vô hạn. Sang ngày UTC mới thì hạn mức được mở lại.
func TestEvaluator_FailedCallsCountTowardLimitUntilNextDay(t *testing.T) {
	f := newEvalFixture("không phải json")
	f.configs.values[configEvalDailyLimit] = "2"
	now := time.Date(2026, 11, 5, 23, 0, 0, 0, time.UTC)
	f.evaluator.now = func() time.Time { return now }
	for range 5 {
		f.evaluator.process(context.Background(), f.candidate())
	}
	if f.provider.calls != 2 || len(f.store.results) != 0 {
		t.Fatalf("calls = %d, results = %d, muốn 2 và 0", f.provider.calls, len(f.store.results))
	}
	now = now.Add(2 * time.Hour)
	f.evaluator.process(context.Background(), f.candidate())
	if f.provider.calls != 3 {
		t.Fatalf("sang ngày mới calls = %d, muốn 3", f.provider.calls)
	}
}

func TestEvaluator_DisabledDoesNotCallProvider(t *testing.T) {
	f := newEvalFixture(goodResponse)
	f.configs.values[configEvalEnabled] = "false"
	f.evaluator.process(context.Background(), f.candidate())
	if f.provider.calls != 0 || len(f.store.results) != 0 {
		t.Fatalf("calls = %d, results = %d, muốn 0", f.provider.calls, len(f.store.results))
	}
}

func TestEvaluator_ConfigReadErrorKeepsDefaults(t *testing.T) {
	f := newEvalFixture(goodResponse)
	f.configs.getErr = errors.New("db down")
	f.evaluator.process(context.Background(), f.candidate())
	if f.provider.calls != 1 {
		t.Fatalf("calls = %d, muốn 1 vì mặc định là bật", f.provider.calls)
	}
}

func TestEvaluator_CountErrorSkipsCall(t *testing.T) {
	f := newEvalFixture(goodResponse)
	f.store.countErr = errors.New("db down")
	f.evaluator.process(context.Background(), f.candidate())
	if f.provider.calls != 0 {
		t.Fatalf("calls = %d, muốn 0 vì không đếm được hạn mức", f.provider.calls)
	}
}

func TestEvaluator_ProviderFailureWritesNoScore(t *testing.T) {
	cases := map[string]func() (*providers.ChatResponse, error){
		"lỗi provider":    func() (*providers.ChatResponse, error) { return nil, errors.New("boom") },
		"panic":           func() (*providers.ChatResponse, error) { panic("boom") },
		"rỗng":            respond(""),
		"văn bản":         respond("Tôi nghĩ là tốt"),
		"thiếu used":      respond(`{"relevant_items":1}`),
		"thiếu số mục":    respond(`{"used":true}`),
		"sai kiểu số":     respond(`{"relevant_items":"2","used":true}`),
		"số không nguyên": respond(`{"relevant_items":1.5,"used":true}`),
		"sai kiểu bool":   respond(`{"relevant_items":1,"used":"true"}`),
		"hai đối tượng":   respond(`{"relevant_items":1,"used":true} {"relevant_items":2,"used":false}`),
		"reason sai kiểu": respond(`{"relevant_items":1,"used":true,"reason":5}`),
	}
	for name, chat := range cases {
		t.Run(name, func(t *testing.T) {
			f := newEvalFixture("")
			f.provider.chatFn = chat
			f.evaluator.process(context.Background(), f.candidate())
			if len(f.store.results) != 0 {
				t.Fatalf("không được ghi điểm: %+v", f.store.results)
			}
		})
	}
}

func TestEvaluator_QueueFullDoesNotBlock(t *testing.T) {
	f := newEvalFixture(goodResponse)
	done := make(chan int, 1)
	go func() {
		accepted := 0
		for range evalQueueSize + 10 {
			if f.evaluator.Submit(f.candidate()) {
				accepted++
			}
		}
		done <- accepted
	}()
	select {
	case accepted := <-done:
		if accepted != evalQueueSize {
			t.Fatalf("accepted = %d, muốn %d", accepted, evalQueueSize)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Submit bị chặn khi hàng đợi đầy")
	}
	var nilEvaluator *Evaluator
	if nilEvaluator.Submit(f.candidate()) {
		t.Fatal("nil receiver không được nhận mẫu")
	}
}

func TestEvaluator_StartStopProcessesQueue(t *testing.T) {
	f := newEvalFixture(goodResponse)
	f.evaluator.Start()
	f.evaluator.Start()
	f.evaluator.Submit(f.candidate())
	deadline := time.Now().Add(5 * time.Second)
	for f.provider.callCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	f.evaluator.Stop()
	f.evaluator.Stop()
	if f.provider.callCount() != 1 {
		t.Fatalf("calls = %d, muốn 1", f.provider.callCount())
	}
}
