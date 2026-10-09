/**
 * The address of one register view, built in one place for filters, pager and
 * redirect. The address is the state.
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
  /** Drop the page when the query changes, so a new search starts at page one. */
  resetPage?: boolean;
}): string {
  const params = new URLSearchParams();

  if (query) params.set("q", query);
  if (status) params.set("status", status);
  if (!resetPage && offset > 0) params.set("offset", String(offset));

  return params.size > 0 ? `/users?${params}` : "/users";
}
