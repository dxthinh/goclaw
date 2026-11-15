package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// TaskMetaAcceptanceReport lưu báo cáo tiêu chí nghiệm thu mà member gửi khi
// complete (contract.md mục 3): [{index, met, evidence}].
const TaskMetaAcceptanceReport = "acceptance_report"

// AcceptanceCriterionReport là báo cáo của member cho một tiêu chí nghiệm thu.
// Index đếm từ 1 theo thứ tự trong contract.acceptance.
type AcceptanceCriterionReport struct {
	Index    int    `json:"index"`
	Met      bool   `json:"met"`
	Evidence string `json:"evidence,omitempty"`
}

// AcceptanceVerdict là kết quả đối chiếu báo cáo với contract.
type AcceptanceVerdict int

const (
	// AcceptanceAllMet: mọi tiêu chí đều được báo cáo là đạt.
	AcceptanceAllMet AcceptanceVerdict = iota
	// AcceptanceMissing: có tiêu chí không được báo cáo (kể cả khi không gửi criteria).
	AcceptanceMissing
	// AcceptanceUnmet: đủ mục nhưng có tiêu chí được báo cáo là chưa đạt.
	AcceptanceUnmet
)

// AcceptanceCheck là kết quả của CheckAcceptance. Missing và Unmet là index
// (đếm từ 1) theo thứ tự tăng dần.
type AcceptanceCheck struct {
	Verdict AcceptanceVerdict
	Missing []int
	Unmet   []int
}

// ParseAcceptanceCriteria đọc tham số criteria của complete: mảng
// {index, met, evidence}. Chấp nhận số dạng float64 (JSON) hoặc chuỗi số,
// met dạng bool hoặc chuỗi "true"/"false". Kiểm tra index nằm trong
// [1, acceptanceCount], không trùng, evidence không quá giới hạn. Lỗi trả về
// là tiếng Anh cho LLM sửa được ngay. raw nil trả nil, nil.
func ParseAcceptanceCriteria(raw any, acceptanceCount int) ([]AcceptanceCriterionReport, error) {
	if raw == nil {
		return nil, nil
	}
	if s, ok := raw.(string); ok {
		if strings.TrimSpace(s) == "" {
			return nil, nil
		}
		var v []any
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			return nil, errors.New("criteria must be an array of {index, met, evidence}")
		}
		raw = v
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, errors.New("criteria must be an array of {index, met, evidence}")
	}
	if len(items) > taskContractMaxItems {
		return nil, fmt.Errorf("criteria has more than %d items", taskContractMaxItems)
	}
	out := make([]AcceptanceCriterionReport, 0, len(items))
	seen := make(map[int]bool, len(items))
	for i, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("criteria[%d] must be an object {index, met, evidence}", i)
		}
		idx, ok := criterionIndex(m["index"])
		if !ok || idx < 1 || idx > acceptanceCount {
			return nil, fmt.Errorf("criteria[%d].index must be a number from 1 to %d (the acceptance criterion it reports on)", i, acceptanceCount)
		}
		if seen[idx] {
			return nil, fmt.Errorf("acceptance criterion %d is reported more than once", idx)
		}
		seen[idx] = true
		met, ok := criterionMet(m["met"])
		if !ok {
			return nil, fmt.Errorf("criteria[%d].met must be true or false", i)
		}
		evidence, _ := m["evidence"].(string)
		evidence = strings.TrimSpace(evidence)
		if utf8.RuneCountInString(evidence) > taskContractMaxString {
			return nil, fmt.Errorf("criteria[%d].evidence exceeds %d characters", i, taskContractMaxString)
		}
		out = append(out, AcceptanceCriterionReport{Index: idx, Met: met, Evidence: evidence})
	}
	slices.SortFunc(out, func(a, b AcceptanceCriterionReport) int { return a.Index - b.Index })
	return out, nil
}

func criterionIndex(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		if n != float64(int(n)) {
			return 0, false
		}
		return int(n), true
	case int:
		return n, true
	case string:
		var i int
		if _, err := fmt.Sscanf(strings.TrimSpace(n), "%d", &i); err != nil {
			return 0, false
		}
		return i, true
	}
	return 0, false
}

func criterionMet(v any) (bool, bool) {
	switch b := v.(type) {
	case bool:
		return b, true
	case string:
		switch strings.ToLower(strings.TrimSpace(b)) {
		case "true":
			return true, true
		case "false":
			return false, true
		}
	}
	return false, false
}

// CheckAcceptance đối chiếu báo cáo của member với contract.acceptance. Thiếu
// mục được ưu tiên hơn chưa đạt: cả hai đều đưa task về chờ lead, nhưng thiếu
// mục là lỗi của báo cáo chứ không phải của sản phẩm.
func CheckAcceptance(c *TaskContract, criteria []AcceptanceCriterionReport) AcceptanceCheck {
	var res AcceptanceCheck
	if c == nil {
		return res
	}
	byIndex := make(map[int]AcceptanceCriterionReport, len(criteria))
	for _, r := range criteria {
		byIndex[r.Index] = r
	}
	for i := 1; i <= len(c.Acceptance); i++ {
		r, ok := byIndex[i]
		switch {
		case !ok:
			res.Missing = append(res.Missing, i)
		case !r.Met:
			res.Unmet = append(res.Unmet, i)
		}
	}
	switch {
	case len(res.Missing) > 0:
		res.Verdict = AcceptanceMissing
	case len(res.Unmet) > 0:
		res.Verdict = AcceptanceUnmet
	default:
		res.Verdict = AcceptanceAllMet
	}
	return res
}

// FormatAcceptanceReport dựng bảng tiêu chí nối vào result để lead thấy bằng
// chứng (tiếng Anh, cho LLM).
func FormatAcceptanceReport(c *TaskContract, criteria []AcceptanceCriterionReport) string {
	if c == nil || len(c.Acceptance) == 0 {
		return ""
	}
	byIndex := make(map[int]AcceptanceCriterionReport, len(criteria))
	for _, r := range criteria {
		byIndex[r.Index] = r
	}
	var sb strings.Builder
	sb.WriteString("Acceptance criteria:\n")
	for i, a := range c.Acceptance {
		r, ok := byIndex[i+1]
		mark := "NOT REPORTED"
		switch {
		case ok && r.Met:
			mark = "MET"
		case ok:
			mark = "NOT MET"
		}
		fmt.Fprintf(&sb, "%d. [%s] %s", i+1, mark, contractLine(a))
		if ok && r.Evidence != "" {
			sb.WriteString(" — evidence: " + contractLine(r.Evidence))
		}
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// acceptanceReportMetadata đổi báo cáo sang dạng lưu trong metadata (cùng
// hình dạng trước và sau khi qua JSON của store).
func acceptanceReportMetadata(criteria []AcceptanceCriterionReport) []any {
	out := make([]any, 0, len(criteria))
	for _, r := range criteria {
		m := map[string]any{"index": r.Index, "met": r.Met}
		if r.Evidence != "" {
			m["evidence"] = r.Evidence
		}
		out = append(out, m)
	}
	return out
}

// GatedTaskContract trả contract của task khi các cổng nghiệm thu áp dụng:
// team đã bật task_contract (luôn tắt ở Lite) và task có contract. Trả nil
// khi task chạy như cũ (tự hoàn thành, complete chỉ cần result). Tắt
// task_contract của team là cách rollback, nên cổng phụ thuộc cả cài đặt team.
func GatedTaskContract(team *store.TeamData, task *store.TeamTaskData) *TaskContract {
	if team == nil || !ParseTaskContractConfig(team.Settings).Enabled {
		return nil
	}
	return TaskContractFromTask(task)
}

// SubmitResultForLeadReview chuyển task in_progress sang in_review kèm result
// và checkpoint_pending = "lead" (cộng extraMeta) trong cùng một câu UPDATE,
// rồi cập nhật bản task trong bộ nhớ. Người gọi phát sự kiện. Dùng khi task có
// contract mà báo cáo tiêu chí thiếu, có mục chưa đạt, hoặc lượt chạy kết thúc
// không gọi complete.
func SubmitResultForLeadReview(ctx context.Context, ts store.TeamStore, team *store.TeamData, task *store.TeamTaskData, result string, extraMeta map[string]any) error {
	patch := map[string]any{TaskMetaCheckpointPending: CheckpointPendingLead}
	for k, v := range extraMeta {
		patch[k] = v
	}
	if err := ts.SubmitTaskResultForReview(ctx, task.ID, team.ID, result, patch); err != nil {
		return fmt.Errorf("failed to submit result for the lead's review: %w", err)
	}
	if task.Metadata == nil {
		task.Metadata = make(map[string]any, len(patch))
	}
	for k, v := range patch {
		task.Metadata[k] = v
	}
	task.Status = store.TeamTaskStatusInReview
	task.Result = &result
	return nil
}

// TaskContractMaxIterations trả limits.max_iterations của contract (0 khi
// không đặt).
func TaskContractMaxIterations(c *TaskContract) int {
	if c == nil || c.Limits == nil || c.Limits.MaxIterations < 0 {
		return 0
	}
	return c.Limits.MaxIterations
}

// ClampContractMaxIterations hạ limits.max_iterations xuống giới hạn của agent
// (agentMax > 0). Trả giá trị cũ và true khi đã hạ.
func ClampContractMaxIterations(c *TaskContract, agentMax int) (from int, clamped bool) {
	cur := TaskContractMaxIterations(c)
	if agentMax <= 0 || cur <= agentMax {
		return cur, false
	}
	c.Limits.MaxIterations = agentMax
	return cur, true
}
