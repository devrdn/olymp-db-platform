import { describe, expect, test } from "vitest";

import { readableBytes, readableDuration } from "./bytes";

describe("readableBytes", () => {
  test("scales to the unit a person reads", () => {
    expect(readableBytes(0)).toBe("0 B");
    expect(readableBytes(900)).toBe("900 B");
    expect(readableBytes(4 * 1024 * 1024)).toBe("4.0 MiB");
    expect(readableBytes(1536)).toBe("1.5 KiB");
    expect(readableBytes(64 * 1024 * 1024)).toBe("64 MiB");
    expect(readableBytes(3 * 1024 * 1024 * 1024)).toBe("3.0 GiB");
  });
});

describe("readableDuration", () => {
  test("reads as a clock, minutes and seconds", () => {
    expect(readableDuration(0)).toBe("0:00");
    expect(readableDuration(7)).toBe("0:07");
    expect(readableDuration(127)).toBe("2:07");
  });

  test("carries an hour once there is one to show", () => {
    expect(readableDuration(3727)).toBe("1:02:07");
  });

  // An unsettled rate can produce a negative or infinite estimate.
  test("never reads negative", () => {
    expect(readableDuration(-5)).toBe("0:00");
  });
});
