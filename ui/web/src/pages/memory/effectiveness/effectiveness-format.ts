/** Định dạng phần trăm; null nghĩa là không có dữ liệu và do nơi gọi hiển thị chữ i18n. */
export function formatPercent(value: number | null): string | null {
  return value === null ? null : `${(value * 100).toFixed(1)}%`;
}

export function formatCount(value: number): string {
  return Math.round(value).toLocaleString();
}
