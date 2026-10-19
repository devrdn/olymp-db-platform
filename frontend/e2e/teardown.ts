import { clearSeed, readSeed, signInAsOperator } from "./api";

/**
 * The global teardown: closes what the seed opened, so a run against a
 * longer-lived stack leaves an archived contest and two deleted (restorable)
 * accounts behind rather than a running contest and live credentials. Every
 * step is attempted even if an earlier one fails, and a failure fails the run.
 * It acts as the stack's administrator, because an account cannot delete
 * itself.
 */
export default async function teardown(): Promise<void> {
  let seed;
  try {
    seed = readSeed();
  } catch {
    return; // The seed never finished; there is nothing of ours to close.
  }

  const operator = await signInAsOperator();
  const failures: string[] = [];
  const attempt = async (what: string, step: () => Promise<unknown>) => {
    try {
      await step();
    } catch (error) {
      failures.push(`${what}: ${error instanceof Error ? error.message : String(error)}`);
    }
  };

  try {
    const at = `/contests/${seed.contest.id}`;
    const reason = { reason: "Created by the browser smoke test" };
    await attempt("finish the contest", () => operator.call("POST", `${at}/status`, { status: "finished" }));
    await attempt("archive the contest", () => operator.call("POST", `${at}/status`, { status: "archived" }));
    await attempt("delete the participant", () => operator.call("POST", `/users/${seed.participant.id}/delete`, reason));
    await attempt("delete the administrator", () => operator.call("POST", `/users/${seed.admin.id}/delete`, reason));
  } finally {
    await operator.close();
  }
  // Kept on failure: the ids in it are what is left to close by hand.
  if (failures.length > 0) throw new Error(`teardown: ${failures.join("; ")}`);
  clearSeed();
}
