package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Kênh truy hồi được chấm, lưu ở cột channel của memory_eval_results.
const (
	MemoryEvalChannelAutoInject     = "auto_inject"
	MemoryEvalChannelMemorySearch   = "memory_search"
	MemoryEvalChannelKnowledgeGraph = "knowledge_graph_search"
)

// MemoryKPIGaugeMetricList liệt kê các metric giá trị cuối ngày, dạng hằng để ghép vào IN (...).
// Các metric còn lại trong memory_kpi_daily đều là cộng dồn.
const MemoryKPIGaugeMetricList = `'episodic_total','episodic_stale','episodic_promoted','kg_entities','kg_relations','memory_documents','dreaming_documents'`

// ErrMemoryKPITenantRequired được trả khi thao tác theo tenant không có tenant trong ctx.
var ErrMemoryKPITenantRequired = errors.New("memory_kpi: tenant_id required in context")

// MemoryEvalResult là kết quả chấm một mẫu. Không chứa câu hỏi, memory hay câu trả lời;
// Reason đã được làm sạch và tối đa 200 ký tự. Tenant lấy từ ctx khi ghi.
type MemoryEvalResult struct {
	ID                uuid.UUID
	AgentID           uuid.UUID
	Channel           string
	HasEpisodic       bool
	HasDocument       bool
	HasKnowledgeGraph bool
	ItemCount         int
	RelevantItems     int
	Used              bool
	Reason            string
	Provider          string
	Model             string
	InputTokens       int64
	OutputTokens      int64
	TraceID           *uuid.UUID
	CreatedAt         time.Time
}

// MemoryKPIDaily là một dòng rollup dạng dài theo ngày UTC.
type MemoryKPIDaily struct {
	TenantID   uuid.UUID `json:"tenant_id"`
	BucketDate time.Time `json:"bucket_date"`
	AgentID    uuid.UUID `json:"agent_id"`
	Metric     string    `json:"metric"`
	Dimension  string    `json:"dimension"`
	Value      float64   `json:"value"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// MemoryKPIPruneCutoff là mốc xóa của từng loại dữ liệu thô, vì hai loại có hạn giữ khác nhau.
type MemoryKPIPruneCutoff struct {
	Metrics     time.Time // metric prompt và retrieval/auto_inject
	EvalResults time.Time // memory_eval_results
}

// MemoryKPIStore lưu kết quả chấm và rollup KPI memory.
type MemoryKPIStore interface {
	// InsertEvalResult ghi một kết quả chấm cho tenant trong ctx.
	InsertEvalResult(ctx context.Context, result MemoryEvalResult) error
	// CountEvalsSince đếm số lượt chấm của tenant trong ctx từ mốc since.
	CountEvalsSince(ctx context.Context, since time.Time) (int, error)
	// RefreshDailyRollup tính lại một ngày UTC xuyên tenant trong một transaction, chỉ dành
	// cho worker nền. Dòng cộng dồn luôn được thay; dòng giá trị cuối ngày chỉ đổi khi
	// includeGauges=true.
	RefreshDailyRollup(ctx context.Context, day time.Time, includeGauges bool) error
	// MissingRollupDays trả các ngày trong days chưa có dòng cộng dồn nào ở mọi tenant, chỉ
	// dành cho worker nền nên không lọc theo tenant và chỉ trả về ngày, không trả số liệu.
	MissingRollupDays(ctx context.Context, days []time.Time) ([]time.Time, error)
	// PruneRawMetrics xóa dữ liệu thô quá hạn, trả số dòng đã xóa. Không đụng rollup hay
	// các metric khác.
	PruneRawMetrics(ctx context.Context, before MemoryKPIPruneCutoff) (int64, error)
	// QueryDaily đọc rollup của tenant trong ctx, from và to tính cả hai đầu. agentID nil
	// nghĩa là mọi agent.
	QueryDaily(ctx context.Context, agentID *uuid.UUID, from, to time.Time) ([]MemoryKPIDaily, error)
}

// MemoryKPIDay chuẩn hóa thời điểm về 00:00 UTC của ngày đó.
func MemoryKPIDay(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// MemoryKPIDayKey là khóa ngày dạng YYYY-MM-DD theo UTC.
func MemoryKPIDayKey(t time.Time) string { return t.UTC().Format(time.DateOnly) }

// MemoryKPIDayRange trả ngày nhỏ nhất và lớn nhất của danh sách không rỗng.
func MemoryKPIDayRange(days []time.Time) (minDay, maxDay time.Time) {
	minDay, maxDay = MemoryKPIDay(days[0]), MemoryKPIDay(days[0])
	for _, day := range days[1:] {
		d := MemoryKPIDay(day)
		if d.Before(minDay) {
			minDay = d
		}
		if d.After(maxDay) {
			maxDay = d
		}
	}
	return minDay, maxDay
}

// MemoryKPIDaysExcept giữ thứ tự của days và bỏ các ngày có khóa trong present.
func MemoryKPIDaysExcept(days []time.Time, present map[string]bool) []time.Time {
	var missing []time.Time
	for _, day := range days {
		if d := MemoryKPIDay(day); !present[MemoryKPIDayKey(d)] {
			missing = append(missing, d)
		}
	}
	return missing
}

// MemoryKPIInsertSQL dựng câu INSERT từ CTE agg trong withClause. agg phải có các cột
// tenant_id, agent_id, dimension; metrics là các cặp {tên metric, cột trong agg}. Mọi tham
// số đều là hằng trong code của từng dialect, giá trị ngoài đi qua placeholder trong dayExpr.
func MemoryKPIInsertSQL(withClause, dayExpr, nowExpr string, metrics ...[2]string) string {
	var sb strings.Builder
	sb.WriteString(withClause)
	sb.WriteString("\nINSERT INTO memory_kpi_daily (tenant_id, bucket_date, agent_id, metric, dimension, value, updated_at)\n")
	for i, m := range metrics {
		if i > 0 {
			sb.WriteString("UNION ALL\n")
		}
		fmt.Fprintf(&sb, "SELECT tenant_id, %s, agent_id, '%s', dimension, %s, %s FROM agg\n", dayExpr, m[0], m[1], nowExpr)
	}
	return sb.String()
}
