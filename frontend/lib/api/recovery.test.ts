import { describe, expect, test } from "vitest";

import { forgotAnswerSchema, isResetToken } from "./recovery";

describe("the recovery answers", () => {
  test("reads the one answer every request for a link gets", () => {
    expect(forgotAnswerSchema.parse({ status: "link_sent" })).toEqual({ status: "link_sent" });
  });

  test("refuses an answer that is not the contract", () => {
    expect(() => forgotAnswerSchema.parse({ status: "sent" })).toThrow();
  });
});

describe("isResetToken", () => {
  /** 32 random bytes, unpadded base64url, as recovery.TokenLength says. */
  test("accepts a token of the length and alphabet links carry", () => {
    expect(isResetToken("A".repeat(21) + "-_" + "z9".repeat(10))).toBe(true);
  });

  test.each(["", "short", "A".repeat(42), "A".repeat(44), "A".repeat(42) + "=", "A".repeat(42) + "/"])(
    "refuses %j",
    (token) => {
      expect(isResetToken(token)).toBe(false);
    },
  );
});
