//go:build integration || sqliteonly

package integration

// wantCumulative là kết quả tính tay từ seedCumulative. Tiền tố zero: là khóa agent UUID 0.
func wantCumulative() map[string]float64 {
	return map[string]float64{
		"retrieval_events|auto_inject:injected":          2,
		"retrieval_events|auto_inject:no_match":          1,
		"retrieval_events|memory_search:matched":         1,
		"retrieval_events|memory_search:unknown":         1,
		"retrieval_events|knowledge_graph_search:called": 2,
		"eval_samples|auto_inject":                       1,
		"eval_items|auto_inject":                         4,
		"eval_relevant_items|auto_inject":                3,
		"eval_used|auto_inject":                          1,
		"eval_samples|memory_search":                     1,
		"eval_items|memory_search":                       2,
		"eval_relevant_items|memory_search":              1,
		"eval_used|memory_search":                        0,
		"eval_samples|knowledge_graph":                   1,
		"eval_items|knowledge_graph":                     5,
		"eval_relevant_items|knowledge_graph":            5,
		"eval_used|knowledge_graph":                      1,
		"eval_samples|episodic":                          2,
		"eval_items|episodic":                            6,
		"eval_relevant_items|episodic":                   4,
		"eval_used|episodic":                             1,
		"eval_samples|document":                          1,
		"eval_items|document":                            2,
		"eval_relevant_items|document":                   1,
		"eval_used|document":                             0,
		"prompt_runs|":                                   2,
		"prompt_system_tokens|":                          300,
		"prompt_tool_schema_tokens|":                     50,
		"prompt_history_tokens|":                         400,
		"prompt_tool_result_tokens|":                     100,
		"prompt_memory_tokens|":                          20,
		"llm_calls|anthropic/m1":                         2,
		"llm_input_tokens|anthropic/m1":                  1200,
		"llm_cache_read_tokens|anthropic/m1":             450,
		"llm_cache_denominator_tokens|anthropic/m1":      1750, // 1000+400+100 + 200+50
		"llm_cache_reporting_calls|anthropic/m1":         2,
		"llm_calls|openai/m2":                            1,
		"llm_input_tokens|openai/m2":                     1000,
		"llm_cache_read_tokens|openai/m2":                600,
		"llm_cache_denominator_tokens|openai/m2":         1000, // input đã gồm phần cache
		"llm_cache_reporting_calls|openai/m2":            1,
		"llm_calls|plain/m3":                             1,
		"llm_input_tokens|plain/m3":                      500,
		"llm_cache_read_tokens|plain/m3":                 0,
		"llm_cache_denominator_tokens|plain/m3":          500,
		"llm_cache_reporting_calls|plain/m3":             0, // provider không báo cache
		"bg_calls|episodic-summary":                      2,
		"bg_input_tokens|episodic-summary":               30,
		"bg_output_tokens|episodic-summary":              3,
		"zero:bg_calls|memory-eval":                      1,
		"zero:bg_input_tokens|memory-eval":               5,
		"zero:bg_output_tokens|memory-eval":              7,
	}
}

// wantGauges là kết quả tính tay từ seedGauges.
func wantGauges() map[string]float64 {
	return map[string]float64{
		"episodic_total|":     3,
		"episodic_promoted|":  1,
		"episodic_stale|":     1,
		"kg_entities|":        2,
		"kg_relations|":       1,
		"memory_documents|":   3,
		"dreaming_documents|": 1,
	}
}
