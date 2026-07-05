import { describe, it, expect } from "vitest";
import { defaultSettings, parseSettings } from "./settings";

describe("settings", () => {
  it("defaults passBrowserShortcuts to false (faithful terminal)", () => {
    expect(defaultSettings().passBrowserShortcuts).toBe(false);
  });

  it("parses stored JSON and falls back to defaults", () => {
    expect(parseSettings(null).passBrowserShortcuts).toBe(false);
    expect(parseSettings("{not json").passBrowserShortcuts).toBe(false);
    expect(parseSettings('{"passBrowserShortcuts":true}').passBrowserShortcuts).toBe(true);
    // Missing keys default; unknown keys are ignored.
    expect(parseSettings('{"foo":1}').passBrowserShortcuts).toBe(false);
  });

  it("defaults newPaneSide to auto and round-trips a stored value", () => {
    expect(defaultSettings().newPaneSide).toBe("auto");
    expect(parseSettings('{"newPaneSide":"left"}').newPaneSide).toBe("left");
    // Missing key falls back to the default.
    expect(parseSettings("{}").newPaneSide).toBe("auto");
  });
});
