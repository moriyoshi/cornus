import { describe, it, expect } from "vitest";
import { defaultSettings, parseSettings } from "./settings";

describe("settings", () => {
  it("defaults passBrowserShortcuts to false (faithful terminal)", () => {
    expect(defaultSettings().passBrowserShortcuts).toBe(false);
  });

  // ON by default: the numbers are what tells two identically-labelled tabs apart, and a
  // setting nobody has found yet should leave the feature working. The other half matters
  // more — a blob saved before this setting existed has no such key, and defaulting it to
  // `false` there would silently turn the numbers off for every existing user.
  it("defaults the tab pane numbers on, including for a blob saved before it existed", () => {
    expect(defaultSettings().paneNumbersInTabs).toBe(true);
    expect(parseSettings('{"passBrowserShortcuts":true}').paneNumbersInTabs).toBe(true);
    expect(parseSettings('{"paneNumbersInTabs":false}').paneNumbersInTabs).toBe(false);
  });

  // "ask" is the default and a blob saved before the setting existed must get it, because
  // the alternative is a UI that silently stopped asking — the one outcome nobody chose.
  //
  // The last line records what parseSettings does NOT do: it spreads stored JSON over the
  // defaults without validating, so a junk value survives into the parsed object. That is
  // deliberate here rather than merely tolerated, and it is why beginPlace matches its two
  // answers POSITIVELY — an unrecognised disposition has to land on asking, and the place
  // that decides so is the reader, not this parser.
  it("defaults the new-pane placement to asking, including for an older blob", () => {
    expect(defaultSettings().newPaneDisposition).toBe("ask");
    expect(parseSettings('{"prefixEnabled":false}').newPaneDisposition).toBe("ask");
    expect(parseSettings('{"newPaneDisposition":"tab"}').newPaneDisposition).toBe("tab");
    expect(parseSettings('{"newPaneDisposition":"sidebyside"}').newPaneDisposition).toBe(
      "sidebyside" as never,
    );
  });

  // The opposite default to the tab numbers above, and the reason is the asymmetry between
  // them: leaving the numbers on costs a user who never finds the setting nothing, while
  // leaving pane zoom on would take the browser's own pinch away from three kinds of pane
  // for someone who never asked. A blob saved before it existed is such a someone.
  it("defaults pane zoom off, including for a blob saved before it existed", () => {
    expect(defaultSettings().paneZoom).toBe(false);
    expect(parseSettings('{"paneNumbersInTabs":false}').paneZoom).toBe(false);
    expect(parseSettings('{"paneZoom":true}').paneZoom).toBe(true);
  });

  it("parses stored JSON and falls back to defaults", () => {
    expect(parseSettings(null).passBrowserShortcuts).toBe(false);
    expect(parseSettings("{not json").passBrowserShortcuts).toBe(false);
    expect(parseSettings('{"passBrowserShortcuts":true}').passBrowserShortcuts).toBe(true);
    // Missing keys default; unknown keys are ignored.
    expect(parseSettings('{"foo":1}').passBrowserShortcuts).toBe(false);
  });

  // A blob written before a setting was removed must still load. The placement-side
  // preference went out with the prompt it fed, and someone's localStorage still holds it.
  it("still loads a blob written before a setting was removed", () => {
    const old = '{"passBrowserShortcuts":true,"newPaneSide":"left"}';
    expect(parseSettings(old).passBrowserShortcuts).toBe(true);
    // Note what does NOT happen: parseSettings spreads the stored object over the
    // defaults, so the dead key rides along in the result rather than being stripped. That
    // is harmless — nothing reads it — but it is worth stating, because the tempting
    // assertion (`toEqual(defaultSettings())`) is false and would have been written from a
    // guess about how the merge works.
    expect(defaultSettings()).not.toHaveProperty("newPaneSide");
  });
});
