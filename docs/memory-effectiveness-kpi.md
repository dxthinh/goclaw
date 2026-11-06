# KPI hiệu quả memory

Tài liệu này mô tả các chỉ số mà `GET /v1/memory/effectiveness` và tab Effectiveness của trang Memory hiển thị: định nghĩa, công thức, nguồn dữ liệu và câu SQL PostgreSQL tái tạo đúng số mà API trả về từ bảng `memory_kpi_daily`.

## 1. Cách số liệu được tạo ra

```text
agent_evolution_metrics, memory_eval_results, spans/traces, usage_events, bảng nguồn memory
        │  worker rollup, mỗi giờ một lần
        ▼
memory_kpi_daily (tenant_id, bucket_date, agent_id, metric, dimension, value)
        │  QueryDaily + buildEffectiveness
        ▼
GET /v1/memory/effectiveness
```

- Ngày theo UTC. `bucket_date` là ngày UTC, cửa sổ `days` gồm cả hôm nay: `days=7` là từ hôm nay trừ 6 ngày đến hôm nay. `window.from` và `window.to` cũng là ngày UTC.
- Rollup chạy mỗi giờ nên số có độ trễ. Worker chỉ tính lại hôm nay và hôm qua; hoạt động mới có thể mất tối đa khoảng một giờ để xuất hiện. Ngày cũ hơn không bị ghi đè, nên số vẫn còn khi trace nguồn đã bị xóa.
- Ngày gateway dừng giữa chừng có thể thiếu tối đa khoảng một giờ dữ liệu của ngày đó, vì lần tính lại cuối cùng chưa kịp chạy.
- Lúc khởi động, worker điền các ngày chưa có dòng nào trong 6 ngày gần nhất, chỉ phần cộng dồn. Không lùi tới ngày thứ 7 vì trace của ngày đó đã bị xóa một phần theo hạn giữ, và con số thiếu sẽ bị giữ mãi do backfill không ghi đè ngày đã có dữ liệu. Giá trị cuối ngày (nhóm pipeline) không backfill được vì chỉ đọc được trạng thái hiện tại.
- Metric cộng dồn (phễu, chấm, prompt, cache, LLM nền) được cộng mọi dòng trong cửa sổ. Metric giá trị cuối ngày (nhóm pipeline: `episodic_*`, `kg_*`, `memory_documents`, `dreaming_documents`) không được cộng qua nhiều ngày: API lấy ngày mới nhất có dữ liệu của từng agent, rồi cộng các agent.
- Mọi dòng được lọc theo tenant trong ngữ cảnh xác thực. Thiếu tenant thì API trả 403 và không có số liệu nào.

## 2. Những điều cần biết khi đọc số

- LLM chấm chỉ cho thấy "liên quan" và "được dùng", không chứng minh câu trả lời tốt hơn. Bộ chấm đọc một mẫu memory và đánh giá mục nào liên quan tới câu hỏi, câu trả lời có dùng nó hay không. Không có nhóm đối chứng, nên kết luận chỉ là tín hiệu để quyết định giữ, chỉnh hay tắt một tầng.
- `used_in_reply` không phải KPI "được dùng". Trường này trong `agent_evolution_metrics` chỉ có nghĩa là lần truy hồi có kết quả. KPI "được dùng" chỉ lấy từ `memory_eval_results.used`.
- Run lỗi giữa chừng không có prompt/shape. Metric `prompt/shape` chỉ ghi ở cuối run có ít nhất một lần gọi LLM, nên run bị lỗi giữa chừng không đóng góp vào nhóm Prompt.
- Trên Lite không có token LLM nền qua usage caps. Bản Lite không có dịch vụ usage caps, nên các lệnh gọi LLM nền đi qua đó không được ghi và nhóm "LLM nền" có thể trống hoặc thiếu.
- Số usage tăng sau thay đổi này vì rollup tính cả trace con (delegation, subagent), trước đây bị bỏ qua. Xem mục changelog tương ứng.

## 3. Chuẩn bị SQL

Các câu SQL dưới đây chạy trên PostgreSQL. Đổi `<TENANT_UUID>` thành tenant cần xem; muốn lọc theo agent thì bỏ dấu chú thích ở dòng `agent_id`; muốn đổi cửa sổ thì sửa `6` thành `days - 1` (29 hoặc 89). Chạy khối này một lần trong phiên `psql`, các câu sau dùng lại hai view tạm.

```sql
CREATE TEMP VIEW kpi AS
SELECT k.*
FROM memory_kpi_daily k
WHERE k.tenant_id = '<TENANT_UUID>'
  -- AND k.agent_id = '<AGENT_UUID>'
  AND k.bucket_date BETWEEN ((now() AT TIME ZONE 'UTC')::date - 6) AND (now() AT TIME ZONE 'UTC')::date;

-- Giá trị cuối ngày: với từng agent chỉ giữ ngày mới nhất có dòng giá trị cuối ngày.
CREATE TEMP VIEW kpi_gauge AS
SELECT k.*
FROM kpi k
WHERE k.metric IN ('episodic_total','episodic_stale','episodic_promoted','kg_entities','kg_relations','memory_documents','dreaming_documents')
  AND k.bucket_date = (
    SELECT MAX(g.bucket_date) FROM kpi g
    WHERE g.agent_id = k.agent_id
      AND g.metric IN ('episodic_total','episodic_stale','episodic_promoted','kg_entities','kg_relations','memory_documents','dreaming_documents'));
```

## 4. Từng KPI

### 4.1 Phễu truy hồi (`funnel`)

- Định nghĩa: mỗi kênh một hàng cho biết có bao nhiêu lần truy hồi đủ điều kiện, có kết quả và thực sự được chèn.
- Nguồn: `memory_kpi_daily.metric = 'retrieval_events'`, `dimension = '<channel>:<outcome>'`, rollup từ `agent_evolution_metrics` (retrieval `auto_inject`, retrieval `memory_search`, tool `knowledge_graph_search`). `outcome` của auto-inject: `disabled`, `trivial`, `no_match`, `below_threshold`, `no_abstract`, `injected`, `error`; của memory_search và knowledge_graph_search: `matched`, `no_match`, `error`, `unknown` (dòng cũ không có outcome). Metric của knowledge_graph_search được ghi ở mọi agent, không phụ thuộc cờ `self_evolution_metrics`. Dòng auto-inject cũ không có outcome được tính là `injected`, vì trước đây metric này chỉ được ghi khi đã chèn.
- Công thức (`total` là tổng mọi outcome của kênh):

| Kênh | eligible = searched | matched | injected | disabled | errors |
|---|---|---|---|---|---|
| `auto_inject` | total − `trivial` − `disabled` | `no_abstract` + `injected` | `injected` | `disabled` | `error` |
| `memory_search` | total | `matched` | 0 | 0 | `error` |
| `knowledge_graph_search` | total | `matched` | 0 | 0 | `error` |

API luôn trả đủ ba kênh, kể cả khi bằng 0.

`injected` nghĩa là khối memory đã được dựng cho lượt đó. Nếu request sát giới hạn và đã cạn cách cắt khác, guard có thể bỏ khối này trước khi gửi; lượt đó vẫn được tính là `injected`.

```sql
WITH ch(channel, ord) AS (VALUES ('auto_inject', 1), ('memory_search', 2), ('knowledge_graph_search', 3)),
e AS (
  SELECT split_part(dimension, ':', 1) AS channel, split_part(dimension, ':', 2) AS outcome, SUM(value) AS n
  FROM kpi WHERE metric = 'retrieval_events' GROUP BY 1, 2
)
SELECT ch.channel,
       ROUND(CASE ch.channel WHEN 'auto_inject'
            THEN COALESCE(SUM(e.n) FILTER (WHERE e.outcome NOT IN ('trivial', 'disabled')), 0)
            ELSE COALESCE(SUM(e.n), 0) END) AS eligible,
       ROUND(CASE ch.channel
            WHEN 'auto_inject' THEN COALESCE(SUM(e.n) FILTER (WHERE e.outcome IN ('no_abstract', 'injected')), 0)
            ELSE COALESCE(SUM(e.n) FILTER (WHERE e.outcome = 'matched'), 0) END) AS matched,
       ROUND(CASE WHEN ch.channel = 'auto_inject'
            THEN COALESCE(SUM(e.n) FILTER (WHERE e.outcome = 'injected'), 0) ELSE 0 END) AS injected,
       ROUND(CASE WHEN ch.channel = 'auto_inject'
            THEN COALESCE(SUM(e.n) FILTER (WHERE e.outcome = 'disabled'), 0) ELSE 0 END) AS disabled,
       ROUND(COALESCE(SUM(e.n) FILTER (WHERE e.outcome = 'error'), 0)) AS errors
FROM ch LEFT JOIN e ON e.channel = ch.channel
GROUP BY ch.channel, ch.ord
ORDER BY ch.ord;
```

### 4.2 Kết luận theo tầng (`layers`)

- Định nghĩa: với mỗi tầng `auto_inject`, `memory_search`, `episodic`, `document`, `knowledge_graph`: số mẫu đã chấm, tỷ lệ mục liên quan, tỷ lệ mẫu mà câu trả lời có dùng memory, và nhãn kết luận. API luôn trả đủ năm tầng theo thứ tự trên.
- Nguồn: `memory_eval_results` qua rollup `eval_samples`, `eval_items`, `eval_relevant_items`, `eval_used` (dimension là tên tầng). Một mẫu có thể thuộc nhiều tầng (ví dụ `auto_inject` và `episodic`), nên các dòng không được cộng với nhau.
- Công thức: `relevance = eval_relevant_items / eval_items`, `used = eval_used / eval_samples`; `null` khi mẫu số bằng 0. Nhãn kết luận xem mục 6.

Câu SQL dưới đặt ngưỡng ở CTE `th` với giá trị mặc định `30 / 0.7 / 0.3 / 0.4`; thay bằng giá trị trong `system_configs` nếu tenant đã đổi (mục 6).

```sql
WITH th AS (SELECT 30 AS min_samples, 0.7::float8 AS keep_relevance, 0.3::float8 AS keep_used, 0.4::float8 AS off_relevance),
l(layer, ord) AS (VALUES ('auto_inject', 1), ('memory_search', 2), ('episodic', 3), ('document', 4), ('knowledge_graph', 5)),
a AS (
  SELECT dimension AS layer,
         COALESCE(SUM(value) FILTER (WHERE metric = 'eval_samples'), 0) AS samples,
         COALESCE(SUM(value) FILTER (WHERE metric = 'eval_items'), 0) AS items,
         COALESCE(SUM(value) FILTER (WHERE metric = 'eval_relevant_items'), 0) AS relevant,
         COALESCE(SUM(value) FILTER (WHERE metric = 'eval_used'), 0) AS used_count
  FROM kpi WHERE metric IN ('eval_samples', 'eval_items', 'eval_relevant_items', 'eval_used')
  GROUP BY dimension
), r AS (
  SELECT l.layer, l.ord,
         COALESCE(a.samples, 0) AS samples,
         a.relevant / NULLIF(a.items, 0) AS relevance,
         a.used_count / NULLIF(a.samples, 0) AS used
  FROM l LEFT JOIN a ON a.layer = l.layer
)
SELECT r.layer, ROUND(r.samples) AS samples, r.relevance, r.used,
       CASE WHEN r.samples < th.min_samples OR r.relevance IS NULL OR r.used IS NULL THEN 'insufficient_data'
            WHEN r.relevance < th.off_relevance THEN 'consider_off'
            WHEN r.relevance >= th.keep_relevance AND r.used >= th.keep_used THEN 'keep'
            ELSE 'tune' END AS verdict
FROM r CROSS JOIN th
ORDER BY r.ord;
```

### 4.3 Thành phần prompt (`prompt`)

- Định nghĩa: số token ước tính trung bình mỗi run cho từng thành phần của request: system, tool schema (phần overhead trừ system), history, tool result và memory.
- Nguồn: `agent_evolution_metrics` loại `prompt/shape`, rollup thành `prompt_runs` và `prompt_*_tokens`. Chỉ có ở run có ít nhất một lần gọi LLM. Memory được ghép vào tin nhắn lúc dựng request, và `memory_tokens` đo riêng phần này.
- Công thức: `avg_x = SUM(prompt_x_tokens) / SUM(prompt_runs)`; bằng 0 khi `runs = 0`.

```sql
SELECT ROUND(runs) AS runs,
       CASE WHEN runs > 0 THEN system_t / runs ELSE 0 END      AS system,
       CASE WHEN runs > 0 THEN tool_schema_t / runs ELSE 0 END AS tool_schema,
       CASE WHEN runs > 0 THEN history_t / runs ELSE 0 END     AS history,
       CASE WHEN runs > 0 THEN tool_result_t / runs ELSE 0 END AS tool_result,
       CASE WHEN runs > 0 THEN memory_t / runs ELSE 0 END      AS memory
FROM (
  SELECT COALESCE(SUM(value) FILTER (WHERE metric = 'prompt_runs'), 0) AS runs,
         COALESCE(SUM(value) FILTER (WHERE metric = 'prompt_system_tokens'), 0) AS system_t,
         COALESCE(SUM(value) FILTER (WHERE metric = 'prompt_tool_schema_tokens'), 0) AS tool_schema_t,
         COALESCE(SUM(value) FILTER (WHERE metric = 'prompt_history_tokens'), 0) AS history_t,
         COALESCE(SUM(value) FILTER (WHERE metric = 'prompt_tool_result_tokens'), 0) AS tool_result_t,
         COALESCE(SUM(value) FILTER (WHERE metric = 'prompt_memory_tokens'), 0) AS memory_t
  FROM kpi
) p;
```

### 4.4 Cache prompt (`cache`)

- Định nghĩa: với mỗi cặp provider/model, tỷ lệ token đọc từ cache trên tổng token prompt.
- Nguồn: span `llm_call` (kể cả trace con) qua rollup `llm_calls`, `llm_cache_read_tokens`, `llm_cache_denominator_tokens`, `llm_cache_reporting_calls`, dimension `provider/model`. Mẫu số của một span: nếu metadata có `prompt_tokens_include_cached_segments` thì là `input_tokens`, ngược lại là `input + cache_read + cache_create`. Một span được coi là có báo cache khi metadata có `cache_read_tokens`, `cache_creation_tokens` hoặc `prompt_tokens_include_cached_segments`. Provider không báo cache (`llm_cache_reporting_calls = 0`) thì `cache_rate` là `null`; giao diện hiện "không có dữ liệu" chứ không phải 0%. Model chỉ ghi cache mà không đọc được gì thì hiện 0%, đây là tín hiệu cache đang hỏng.
- Công thức: `cache_rate = SUM(llm_cache_read_tokens) / SUM(llm_cache_denominator_tokens)` khi `reporting` (tổng `llm_cache_reporting_calls` > 0) và mẫu số > 0, ngược lại `null`. Sắp theo `calls` giảm dần.

```sql
SELECT split_part(dimension, '/', 1) AS provider,
       substr(dimension, strpos(dimension, '/') + 1) AS model,
       ROUND(COALESCE(SUM(value) FILTER (WHERE metric = 'llm_calls'), 0)) AS calls,
       COALESCE(SUM(value) FILTER (WHERE metric = 'llm_cache_reporting_calls'), 0) > 0 AS reporting,
       CASE WHEN COALESCE(SUM(value) FILTER (WHERE metric = 'llm_cache_reporting_calls'), 0) > 0
             AND COALESCE(SUM(value) FILTER (WHERE metric = 'llm_cache_denominator_tokens'), 0) > 0
            THEN SUM(value) FILTER (WHERE metric = 'llm_cache_read_tokens')
               / SUM(value) FILTER (WHERE metric = 'llm_cache_denominator_tokens') END AS cache_rate
FROM kpi
WHERE metric IN ('llm_calls', 'llm_cache_reporting_calls', 'llm_cache_read_tokens', 'llm_cache_denominator_tokens')
GROUP BY dimension
ORDER BY calls DESC, provider, model;
```

### 4.5 Pipeline nền (`pipeline`)

Số lượng cuối khoảng: `episodic_total`, `episodic_stale`, `episodic_promoted`, `kg_entities`, `kg_relations`, `memory_documents`, `dreaming_documents` là giá trị cuối ngày, đếm từ bảng nguồn memory lúc rollup. API lấy ngày mới nhất có dữ liệu của từng agent rồi cộng các agent, không cộng qua nhiều ngày.

`episodic_stale` đếm số session cần tóm tắt lại: session có nguồn `session` và số tin nhắn hiện tại đã nhiều hơn `turn_count` của bản tóm tắt phủ xa nhất từ 10 trở lên. Đây là ước lượng: session đã bị cắt bớt tin nhắn có thể có số tin nhắn thấp hơn thực tế nên không bị tính.

```sql
SELECT metric, ROUND(SUM(value)) AS value
FROM kpi_gauge
GROUP BY metric
ORDER BY metric;
```

Token LLM nền theo mục đích (`pipeline.background`): nguồn là `usage_events` với `event_type = 'llm_background'` qua rollup `bg_calls`, `bg_input_tokens`, `bg_output_tokens`, dimension là purpose đã chuẩn hóa. Mỗi purpose một dòng, sắp theo `input_tokens` giảm dần. Dữ liệu chỉ gồm số lượng và token, không có prompt hay user id.

```sql
SELECT dimension AS purpose,
       ROUND(COALESCE(SUM(value) FILTER (WHERE metric = 'bg_calls'), 0)) AS calls,
       ROUND(COALESCE(SUM(value) FILTER (WHERE metric = 'bg_input_tokens'), 0)) AS input_tokens,
       ROUND(COALESCE(SUM(value) FILTER (WHERE metric = 'bg_output_tokens'), 0)) AS output_tokens
FROM kpi
WHERE metric IN ('bg_calls', 'bg_input_tokens', 'bg_output_tokens')
GROUP BY dimension
ORDER BY input_tokens DESC, purpose;
```

## 5. Đối chiếu với bảng nguồn (7 ngày gần nhất)

Hai câu dưới tính trực tiếp từ bảng nguồn cho cùng cửa sổ UTC 7 ngày, theo cùng quy tắc với worker rollup. Kết quả phải khớp mục 4.1 và 4.4, trừ phần độ trễ rollup, phần dữ liệu thô đã bị xóa theo hạn giữ và ngày gateway dừng giữa chừng (mục 1). Thay `<TENANT_UUID>` như ở mục 3. Hai câu này chưa lọc theo agent.

Phễu (so với 4.1):

```sql
WITH win AS (
  SELECT ((now() AT TIME ZONE 'UTC')::date - 6)::timestamp AT TIME ZONE 'UTC' AS from_ts,
         ((now() AT TIME ZONE 'UTC')::date + 1)::timestamp AT TIME ZONE 'UTC' AS to_ts
), src AS (
  SELECT m.metric_key || ':' || COALESCE(NULLIF(m.value->>'outcome', ''),
           CASE WHEN m.metric_key = 'auto_inject' THEN 'injected' ELSE 'unknown' END) AS dimension,
         COUNT(*) AS events
  FROM agent_evolution_metrics m, win
  WHERE m.tenant_id = '<TENANT_UUID>'
    AND m.created_at >= win.from_ts AND m.created_at < win.to_ts
    AND ((m.metric_type = 'retrieval' AND m.metric_key IN ('auto_inject', 'memory_search'))
      OR (m.metric_type = 'tool' AND m.metric_key = 'knowledge_graph_search'))
  GROUP BY 1
)
SELECT dimension, events FROM src ORDER BY dimension;
```

Mỗi dòng `dimension` phải bằng `SUM(value)` của `retrieval_events` cùng dimension trong view `kpi`. Đem các số này qua công thức ở 4.1 sẽ ra đúng bảng phễu.

Cache (so với 4.4, gồm span của trace con):

```sql
WITH win AS (
  SELECT ((now() AT TIME ZONE 'UTC')::date - 6)::timestamp AT TIME ZONE 'UTC' AS from_ts,
         ((now() AT TIME ZONE 'UTC')::date + 1)::timestamp AT TIME ZONE 'UTC' AS to_ts
), per_span AS (
  SELECT COALESCE(s.provider, '') || '/' || COALESCE(s.model, '') AS dimension,
         CASE WHEN jsonb_typeof(s.metadata->'cache_read_tokens') = 'number'
              THEN (s.metadata->>'cache_read_tokens')::float8 ELSE 0 END AS cache_read,
         CASE WHEN jsonb_typeof(s.metadata->'cache_creation_tokens') = 'number'
              THEN (s.metadata->>'cache_creation_tokens')::float8 ELSE 0 END AS cache_create,
         COALESCE(s.input_tokens, 0) AS input_tokens,
         COALESCE(s.metadata->>'prompt_tokens_include_cached_segments' = 'true', false) AS include_cached,
         (s.metadata->'cache_read_tokens' IS NOT NULL
          OR s.metadata->'cache_creation_tokens' IS NOT NULL
          OR s.metadata->'prompt_tokens_include_cached_segments' IS NOT NULL) AS reporting
  FROM spans s
  JOIN traces t ON t.id = s.trace_id
  CROSS JOIN win
  WHERE t.tenant_id = '<TENANT_UUID>'
    AND s.span_type = 'llm_call'
    AND s.created_at >= win.from_ts AND s.created_at < win.to_ts
)
SELECT split_part(dimension, '/', 1) AS provider,
       substr(dimension, strpos(dimension, '/') + 1) AS model,
       COUNT(*) AS calls,
       COUNT(*) FILTER (WHERE reporting) > 0 AS reporting,
       CASE WHEN COUNT(*) FILTER (WHERE reporting) > 0
             AND SUM(CASE WHEN include_cached THEN input_tokens ELSE input_tokens + cache_read + cache_create END) > 0
            THEN SUM(cache_read)
               / SUM(CASE WHEN include_cached THEN input_tokens ELSE input_tokens + cache_read + cache_create END) END AS cache_rate
FROM per_span
GROUP BY dimension
ORDER BY calls DESC, provider, model;
```

## 6. Nhãn kết luận và ngưỡng

Nhãn của một tầng được chọn theo đúng thứ tự sau:

1. `samples < min_samples`: `insufficient_data` (chưa đủ mẫu để kết luận). Cũng dùng khi đủ mẫu nhưng không có mục nào được chấm, vì khi đó không tính được `relevance`.
2. `relevance < off_relevance`: `consider_off` (xem xét tắt tầng này).
3. `relevance >= keep_relevance` và `used >= keep_used`: `keep` (giữ nguyên).
4. Còn lại: `tune` (cần chỉnh: ngưỡng điểm, số kết quả hoặc nội dung memory).

Ngưỡng và công tắc đọc từ `system_configs` theo tenant. Khóa thiếu, không đọc được hoặc giá trị hỏng (không phải số, ngoài `[0,1]` với tỷ lệ, nhỏ hơn 1 với `min_samples`, không phải boolean với công tắc) thì dùng giá trị mặc định.

| Khóa | Mặc định | Ý nghĩa |
|---|---:|---|
| `memory.kpi.min_samples` | `30` | Số mẫu tối thiểu để kết luận |
| `memory.kpi.keep_relevance` | `0.7` | Ngưỡng liên quan để `keep` |
| `memory.kpi.keep_used` | `0.3` | Ngưỡng được dùng để `keep` |
| `memory.kpi.off_relevance` | `0.4` | Dưới mức này thì `consider_off` |
| `memory.auto_inject.enabled` | `true` | Công tắc auto-inject |
| `memory.auto_inject.min_score` | `0.8` | Ngưỡng điểm để chèn memory |
| `memory.eval.enabled` | `true` | Công tắc bộ chấm |
| `memory.eval.daily_limit` | `30` | Số lượt chấm tối đa mỗi ngày, theo tenant |

Đặt công tắc hoặc ngưỡng bằng `PUT /v1/system-configs/{key}` (cần quyền admin, giá trị luôn là chuỗi):

```bash
curl -X PUT "$GATEWAY/v1/system-configs/memory.auto_inject.enabled" \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"value": "false"}'
```

Khi `memory.auto_inject.enabled` hoặc `memory.eval.enabled` là `false`, tab Effectiveness hiện thông báo ở khối phễu, vì số của kênh auto-inject giữ ở 0 hoặc mức liên quan và được dùng không còn được cập nhật.

Điểm auto-inject là `ts_rank` cộng cosine (trọng số 1 và 1). Mặc định 0,8 vì cosine trung vị giữa các episodic của agent khác nhau là 0,73 (p75 0,76), nên vector đơn lẻ không phân biệt được liên quan hay không; `top_score` được ghi ở mọi lượt để hiệu chỉnh ngưỡng.
