import { render, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";

import en from "@/lib/i18n/dictionaries/en";

import { PublishGateReport } from "./publish-gate";

describe("PublishGateReport", () => {
  test("names a problem the dictionary has in the reader's words", () => {
    const code = Object.keys(en.workspace.gate.problems)[0];
    render(<PublishGateReport gate={{ global: [{ code, count: 1 }], byLanguage: [] }} ready={false} dict={en} />);

    expect(screen.getByText(en.workspace.gate.problems[code as keyof typeof en.workspace.gate.problems])).toBeInTheDocument();
  });

  // The code is the server's text, and the dictionary only inherits these.
  test.each(["constructor", "toString"])("shows the code %s as it came, not what the dictionary inherits", (code) => {
    render(<PublishGateReport gate={{ global: [{ code, count: 1 }], byLanguage: [] }} ready={false} dict={en} />);

    expect(screen.getByText(code)).toBeInTheDocument();
  });
});
