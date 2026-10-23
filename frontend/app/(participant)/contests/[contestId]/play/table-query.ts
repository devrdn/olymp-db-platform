/**
 * The statement a click on a table's name writes into the editor
 * (`schema-panel.tsx`).
 */
import { quoteIdentifier } from "@/lib/sql/identifier";

/** How many rows the written statement reads: enough to see a table's shape. */
const PREVIEW_ROWS = 5;

export function tableQuery(table: string): string {
  return `SELECT * FROM ${quoteIdentifier(table)} LIMIT ${PREVIEW_ROWS};`;
}
