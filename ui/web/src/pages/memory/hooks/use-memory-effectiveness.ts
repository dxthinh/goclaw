import { useQuery } from "@tanstack/react-query";
import { useHttp } from "@/hooks/use-ws";
import type { MemoryEffectiveness, MemoryEffectivenessDays } from "@/types/memory-effectiveness";

/** Đọc KPI hiệu quả memory; agentId rỗng nghĩa là tất cả agent. */
export function useMemoryEffectiveness(agentId: string, days: MemoryEffectivenessDays) {
  const http = useHttp();
  const { data, isLoading, isError, isFetching, refetch } = useQuery({
    queryKey: ["memory", "effectiveness", agentId, days],
    queryFn: () => {
      const params: Record<string, string> = { days: String(days) };
      if (agentId) params.agent_id = agentId;
      return http.get<MemoryEffectiveness>("/v1/memory/effectiveness", params);
    },
    // Rollup chạy mỗi giờ nên không cần tải lại thường xuyên.
    staleTime: 60_000,
  });
  return { data, loading: isLoading, error: isError, fetching: isFetching, refetch };
}
