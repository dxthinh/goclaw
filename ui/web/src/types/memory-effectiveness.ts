/** Kết luận của một tầng memory, khớp GET /v1/memory/effectiveness. */
export type MemoryLayerVerdict = "keep" | "tune" | "consider_off" | "insufficient_data";

export interface MemoryEffectivenessFunnel {
  channel: string;
  eligible: number;
  searched: number;
  matched: number;
  injected: number;
  disabled: number;
  errors: number;
}

export interface MemoryEffectivenessLayer {
  layer: string;
  samples: number;
  /** null khi chưa có mục nào được chấm. */
  relevance: number | null;
  /** null khi chưa có mẫu. */
  used: number | null;
  verdict: MemoryLayerVerdict;
}

export interface MemoryEffectivenessCache {
  provider: string;
  model: string;
  calls: number;
  /** null khi provider không báo cache hoặc không có mẫu số. */
  cache_rate: number | null;
  reporting: boolean;
}

export interface MemoryEffectivenessBackground {
  purpose: string;
  calls: number;
  input_tokens: number;
  output_tokens: number;
}

export interface MemoryEffectiveness {
  window: { from: string; to: string; days: number };
  funnel: MemoryEffectivenessFunnel[];
  layers: MemoryEffectivenessLayer[];
  prompt: {
    runs: number;
    avg: { system: number; tool_schema: number; history: number; tool_result: number; memory: number };
  };
  cache: MemoryEffectivenessCache[];
  pipeline: {
    episodic_total: number;
    episodic_stale: number;
    episodic_promoted: number;
    kg_entities: number;
    kg_relations: number;
    memory_documents: number;
    dreaming_documents: number;
    background: MemoryEffectivenessBackground[];
  };
  thresholds: { min_samples: number; keep_relevance: number; keep_used: number; off_relevance: number };
  switches: { auto_inject_enabled: boolean; eval_enabled: boolean };
}

export type MemoryEffectivenessDays = 7 | 30 | 90;
