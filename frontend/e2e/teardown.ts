import { ApiError, clearSeed, readRecord, signInAsOperator, type Api, type SeedRecord } from "./api";

/**
 * The global teardown, which Playwright runs even when the setup threw:
 * closes whatever the setup recorded, so a run against a longer-lived stack
 * leaves an archived (or, if never published, deleted) contest and two
 * deleted, restorable accounts rather than a running contest and live
 * credentials. A failure fails the run and keeps seed.json, which the next
 * setup closes before it starts.
 */
export default async function teardown(): Promise<void> {
  const record = readRecord();
  if (!record) return;

  const operator = await signInAsOperator();
  let failures: string[];
  try {
    failures = await closeRecord(operator, record);
  } finally {
    await operator.close();
  }
  if (failures.length > 0) throw new Error(`teardown: ${failures.join("; ")}`);
  clearSeed();
}

/**
 * Closes every object in `record` that still exists, attempting each even if
 * an earlier one fails, and returns what failed. Safe to repeat: a closed
 * contest and a deleted account are left as they are. Acts as the stack's
 * administrator, because an account cannot delete itself.
 */
export async function closeRecord(operator: Api, record: SeedRecord): Promise<string[]> {
  const failures: string[] = [];
  const attempt = async (what: string, step: () => Promise<unknown>) => {
    try {
      await step();
    } catch (error) {
      if (error instanceof ApiError && error.status === 404) return; // Already gone.
      failures.push(`${what}: ${error instanceof Error ? error.message : String(error)}`);
    }
  };

  if (record.contest) {
    const at = `/contests/${record.contest.id}`;
    await attempt("close the contest", async () => {
      const { status } = await operator.call<{ status: string }>("GET", at);
      // The lifecycle's own steps: a draft can be deleted; anything that was
      // published is a record and can only be archived.
      if (status === "draft") return operator.call("DELETE", at);
      if (status === "running") await operator.call("POST", `${at}/status`, { status: "finished" });
      if (status !== "archived") await operator.call("POST", `${at}/status`, { status: "archived" });
    });
  }

  const reason = { reason: "Created by the browser smoke test" };
  for (const account of [record.participant, record.admin]) {
    if (!account) continue;
    await attempt(`delete ${account.login}`, () => operator.call("POST", `/users/${account.id}/delete`, reason));
  }
  return failures;
}
