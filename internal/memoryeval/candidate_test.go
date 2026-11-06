package memoryeval

import (
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

func baseRun() RunSnapshot {
	return RunSnapshot{TenantID: uuid.New(), AgentID: uuid.New(), TraceID: uuid.New(), Question: "hỏi", Answer: "đáp"}
}

const searchBoth = `{"results":[
	{"tier":"document","path":"a.md","snippet":"đoạn tài liệu"},
	{"tier":"episodic","path":"episodic:s1","snippet":"","l0_abstract":"tóm tắt"},
	{"tier":"episodic","path":"episodic:s2","snippet":"tóm tắt hai"}],"count":3}`

func TestCandidatesFromRun_AutoInject(t *testing.T) {
	run := baseRun()
	run.MemorySection = "<memory_context>\n## Memory Context\n\nReference data recalled from past sessions.\n- mục một\n- mục hai\n</memory_context>"
	got := CandidatesFromRun(run)
	if len(got) != 1 {
		t.Fatalf("candidates = %d, muốn 1", len(got))
	}
	c := got[0]
	if c.Channel != store.MemoryEvalChannelAutoInject || !c.HasEpisodic || c.HasDocument || len(c.Items) != 2 {
		t.Fatalf("ứng viên sai: %+v", c)
	}
	if c.TenantID != run.TenantID || c.AgentID != run.AgentID || c.TraceID != run.TraceID {
		t.Fatalf("định danh sai: %+v", c)
	}
}

func TestCandidatesFromRun_MemorySearchReadsTiers(t *testing.T) {
	run := baseRun()
	run.ToolResults = []ToolResult{{Name: toolMemorySearch, Content: searchBoth}}
	got := CandidatesFromRun(run)
	if len(got) != 1 {
		t.Fatalf("candidates = %d, muốn 1", len(got))
	}
	c := got[0]
	if c.Channel != store.MemoryEvalChannelMemorySearch || !c.HasDocument || !c.HasEpisodic || len(c.Items) != 3 {
		t.Fatalf("ứng viên sai: %+v", c)
	}
	if c.Items[1] != "tóm tắt" {
		t.Fatalf("thiếu snippet phải dùng l0_abstract: %q", c.Items[1])
	}
}

func TestCandidatesFromRun_AtMostOnePerChannelPreferringLatest(t *testing.T) {
	run := baseRun()
	run.ToolResults = []ToolResult{
		{Name: toolMemorySearch, Content: searchBoth},
		{Name: toolKnowledgeGraphSearch, Content: "Found 1 entities matching \"x\":\n\n- Cũ [t] (id: 1)\n"},
		{Name: toolMemorySearch, Content: `{"results":[{"tier":"document","snippet":"mới"}]}`},
		{Name: toolKnowledgeGraphSearch, Content: "Found 1 entities matching \"y\":\n\n- Mới [t] (id: 2)\n"},
		{Name: toolMemorySearch, Content: "No memory results found for query: z"},
	}
	got := CandidatesFromRun(run)
	if len(got) != 2 {
		t.Fatalf("candidates = %d, muốn 2", len(got))
	}
	for _, c := range got {
		if len(c.Items) != 1 {
			t.Fatalf("phải lấy kết quả mới nhất có dữ liệu: %+v", c)
		}
	}
}

func TestCandidatesFromRun_SkipsUnusableResults(t *testing.T) {
	cases := map[string]ToolResult{
		"lỗi tool":      {Name: toolMemorySearch, Content: searchBoth, IsError: true},
		"không kết quả": {Name: toolMemorySearch, Content: "No memory results found for query: x"},
		"json bị cắt":   {Name: toolMemorySearch, Content: `{"results":[{"tier":"doc`},
		"mảng rỗng":     {Name: toolMemorySearch, Content: `{"results":[],"count":0}`},
		"tool khác":     {Name: "web_search", Content: searchBoth},
		"KG rỗng":       {Name: toolKnowledgeGraphSearch, Content: "Knowledge graph is empty. No entities have been extracted yet."},
		// Danh sách gợi ý khi không tìm thấy không phải là kết quả truy hồi.
		"KG gợi ý": {Name: toolKnowledgeGraphSearch, Content: "No entities found matching \"x\". The knowledge graph has 1 entities. Here are some available ones:\n\n- Gợi ý [t] (id: 1)\n"},
	}
	for name, result := range cases {
		t.Run(name, func(t *testing.T) {
			run := baseRun()
			run.ToolResults = []ToolResult{result}
			if got := CandidatesFromRun(run); len(got) != 0 {
				t.Fatalf("không được tạo ứng viên: %+v", got)
			}
		})
	}
}

func TestCandidatesFromRun_RequiresTenantQuestionAndAnswer(t *testing.T) {
	section := "- mục"
	for name, mutate := range map[string]func(*RunSnapshot){
		"thiếu tenant":     func(r *RunSnapshot) { r.TenantID = uuid.Nil },
		"câu hỏi rỗng":     func(r *RunSnapshot) { r.Question = "  " },
		"câu trả lời rỗng": func(r *RunSnapshot) { r.Answer = "\n" },
	} {
		t.Run(name, func(t *testing.T) {
			run := baseRun()
			run.MemorySection = section
			mutate(&run)
			if got := CandidatesFromRun(run); got != nil {
				t.Fatalf("không được tạo ứng viên: %+v", got)
			}
		})
	}
}

func TestKnowledgeGraphItems_Formats(t *testing.T) {
	cases := []struct {
		name, content string
		want          int
	}{
		{"tìm kiếm có quan hệ", "Found 2 entities matching \"x\":\n\n- A [t] (id: 1)\n  mô tả\n  Relations:\n    A —[r]→ B\n- B [t] (id: 2)\n", 2},
		{"duyệt đồ thị", "Graph traversal from \"1\" (max depth 2):\n\n- [depth 1] A (t)\n  path: x → A\n", 1},
		{"kết nối trực tiếp", "Direct connections of \"A\":\n\n  A —[r]→ B\n  A —[s]→ C\n\n(3 more connections not shown)\n", 2},
		{"liệt kê", "Knowledge graph has 1 entities:\n\n- A [t] (id: 1)\n\nTip: Use entity_id parameter.", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := knowledgeGraphItems(tc.content); len(got) != tc.want {
				t.Fatalf("items = %d %q, muốn %d", len(got), got, tc.want)
			}
		})
	}
}
