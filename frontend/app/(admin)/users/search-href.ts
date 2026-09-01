/**
 * The address of one view of the account register.
 *
 * One builder, used by the filters, the pager and the page's own recovery
 * redirect. It was written three times with small differences before, which is
 * how the reset link and the pager end up disagreeing about whether an empty
 * filter belongs in the URL.
 *
 * The address is the state. That is what makes a view shareable, the back
 * button meaningful and the reset an ordinary link — and it is why typing in
 * the search box navigates rather than holding a value in the component.
 */
export function accountsHref({
  query = "",
  status = "",
  offset = 0,
  resetPage = false,
}: {
  query?: string;
  status?: string;
  offset?: number;
  /**
   * Drop the page. Set whenever the question itself changes: staying on page
   * three of the previous result leaves somebody looking at an empty screen
   * that says nothing matched, when plenty did.
   */
  resetPage?: boolean;
}): string {
  const params = new URLSearchParams();

  if (query) params.set("q", query);
  if (status) params.set("status", status);
  if (!resetPage && offset > 0) params.set("offset", String(offset));

  return params.size > 0 ? `/users?${params}` : "/users";
}
