import { describe, it, expect } from "vitest";
import {
  promptText,
  confirmModal,
  promptChoice,
  submitModal,
  dismissModal,
  modalRequest,
} from "./modal";

// The modal service resolves the promise a caller awaits; the host (ModalHost) or a
// test drives submitModal/dismissModal.
describe("modal service", () => {
  it("promptText resolves with the submitted value and clears the request", async () => {
    const p = promptText({ title: "New folder" });
    expect(modalRequest()?.kind).toBe("text");
    submitModal("brandnew");
    expect(await p).toBe("brandnew");
    expect(modalRequest()).toBeNull();
  });

  it("dismiss resolves a text prompt with null and a confirm with false", async () => {
    const t = promptText({ title: "t" });
    dismissModal();
    expect(await t).toBeNull();

    const c = confirmModal({ title: "Delete", danger: true });
    dismissModal();
    expect(await c).toBe(false);
  });

  it("confirmModal resolves true when confirmed", async () => {
    const c = confirmModal({ title: "Delete" });
    submitModal(true);
    expect(await c).toBe(true);
  });

  it("promptChoice resolves the chosen value", async () => {
    const p = promptChoice({ title: "Placement", options: [{ value: "stack", label: "Stack" }] });
    expect(modalRequest()?.kind).toBe("choice");
    submitModal("stack");
    expect(await p).toBe("stack");
  });

  it("opening a new modal supersedes (cancels) the previous one", async () => {
    const first = promptChoice({ title: "a", options: [{ value: "x", label: "X" }] });
    const second = promptText({ title: "b" });
    expect(await first).toBeNull(); // superseded
    submitModal("done");
    expect(await second).toBe("done");
  });
});
