// Pure rule for own-mention highlighting (#1830): a resolved member reference
// is an own mention only when #organization-stream carries a non-empty
// data-member equal to the element's data-member-id.
export function isOwnMention(
  streamMember: string | null | undefined,
  mentionMemberID: string | null | undefined,
): boolean {
  return !!streamMember && mentionMemberID === streamMember;
}
