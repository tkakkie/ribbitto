/** Channel counts already include their topic subtotals. Number is exact up to the cap; larger values only need to exceed it. */
export function tabUnread(channels: readonly string[]): string {
  const total = channels.reduce((sum, count) => sum + (Number(count) || 0), 0);
  return total ? `(${total > 99 ? "99+" : total}) ` : "";
}
