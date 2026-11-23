/** The choices of a results export, apart from the wire schemas (see content-terms.ts for why). */

export const RESULTS_FIELDS = ["full_name", "login", "email"] as const;
export type ResultsField = (typeof RESULTS_FIELDS)[number];

export const RESULTS_DETAILS = ["summary", "detailed"] as const;
export type ResultsDetail = (typeof RESULTS_DETAILS)[number];
