/** Channel counts already include their topic subtotals. */
export function tabUnread(title: string, counts: { channels: readonly string[]; topics?: readonly string[] }): { title: string; badge: boolean } {
  const total = counts.channels.reduce((sum, count) => sum + BigInt(count), 0n);
  return { title: total ? `(${total > 99n ? "99+" : total}) ${title}` : title, badge: total > 0n };
}
