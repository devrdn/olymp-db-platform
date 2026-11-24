import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";

import { ReasonField, useRequiredReason } from "./required-reason";

function Form({ required = true, onSent }: { required?: boolean; onSent: () => void }) {
  const reason = useRequiredReason(required);
  return (
    <form
      noValidate
      onSubmit={(event) => {
        reason.onSubmit(event);
        if (!event.defaultPrevented) {
          event.preventDefault();
          onSent();
        }
      }}
    >
      <ReasonField id="reason" label="Reason" reason={reason} />
      <p>{reason.refusal ?? "fine"}</p>
      <button type="submit">Send</button>
    </form>
  );
}

describe("a required reason", () => {
  test("stops a submission whose reason is empty or only whitespace, and says so", async () => {
    const onSent = vi.fn();
    render(<Form onSent={onSent} />);

    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    await userEvent.type(screen.getByLabelText("Reason"), "   ");
    fireEvent.click(screen.getByRole("button", { name: "Send" }));

    expect(onSent).not.toHaveBeenCalled();
    expect(screen.getByText("reason_required")).toBeInTheDocument();
    expect(screen.getByLabelText("Reason")).toHaveAttribute("aria-invalid", "true");
  });

  test("lets the refusal go as soon as the reason is written, and sends it", async () => {
    const onSent = vi.fn();
    render(<Form onSent={onSent} />);

    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    await userEvent.type(screen.getByLabelText("Reason"), "spam");

    expect(screen.getByText("fine")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    expect(onSent).toHaveBeenCalledOnce();
  });

  test("asks for nothing when the form needs no reason", () => {
    const onSent = vi.fn();
    render(<Form required={false} onSent={onSent} />);

    fireEvent.click(screen.getByRole("button", { name: "Send" }));

    expect(onSent).toHaveBeenCalledOnce();
  });
});
