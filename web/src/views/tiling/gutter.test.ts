import { describe, it, expect } from "vitest";
import { PIN_MIN_VIEWPORT_PX, canPin, directionOf, resolveGutter, startSide } from "./gutter";

// The pinned chooser's three decisions, each a pure function of what it is given. What is
// MEASURED — the viewport's width, the document's direction — is read at one named place in
// ./gutter and handed to these; everything a browser has to be asked about lives in the
// measurement pass, not here.

describe("the pane chooser's gutter", () => {
  // Two independent gates, and the test says so by failing each one with the other satisfied.
  // A single "a phone does not get the pin" case would pass just as well against code that
  // only ever looked at the width.
  describe("who is offered the pin", () => {
    it("wants a fine pointer AND a window with room to spare", () => {
      expect(canPin(false, 1400)).toBe(true);
      // Wide, but a finger is the only pointer there is: a permanent panel on a device with
      // no hover and no cursor is a pane's worth of screen spent on a list.
      expect(canPin(true, 1400)).toBe(false);
      // A mouse, but nothing left over after 22rem of gutter.
      expect(canPin(false, 600)).toBe(false);
      expect(canPin(true, 600)).toBe(false);
    });

    // The threshold is a MINIMUM, so the named width itself is offered the pin. Asserted as a
    // pair one pixel apart, which is the only form that pins down which side of the
    // comparison is inclusive — either bound alone passes against `>` and `>=` alike.
    it("counts the named width as wide enough, and one pixel under as not", () => {
      expect(canPin(false, PIN_MIN_VIEWPORT_PX)).toBe(true);
      expect(canPin(false, PIN_MIN_VIEWPORT_PX - 1)).toBe(false);
    });
  });

  describe("which way the document reads", () => {
    // `dir` is a STATEMENT and the language is an INFERENCE, so the attribute wins — and it
    // wins in both directions. Only the second line rules out "the attribute is consulted
    // when it agrees with the language anyway".
    it("takes an explicit dir over the language, whichever way each points", () => {
      expect(directionOf("rtl", "en-US")).toBe("rtl");
      expect(directionOf("ltr", "ar-EG")).toBe("ltr");
    });

    // `dir="auto"` is not a direction: it tells the browser to work one out from the content.
    // It therefore has to fall through to the language exactly as a missing attribute does,
    // and NOT be treated as "not rtl, so ltr" — which is what a bare inequality would do.
    it("falls through to the language when the attribute says nothing", () => {
      expect(directionOf(null, "he")).toBe("rtl");
      expect(directionOf("", "fa-IR")).toBe("rtl");
      expect(directionOf("auto", "ur")).toBe("rtl");
      expect(directionOf("auto", "en")).toBe("ltr");
    });

    // A language tag arrives with a region, a script, a case and sometimes an underscore, and
    // the answer is a property of the BASE subtag alone. `ar` here, `ar-EG` and `AR_eg` are
    // one language written one way.
    it("reads the base subtag however the tag is spelled", () => {
      expect(directionOf(null, "ar")).toBe("rtl");
      expect(directionOf(null, "ar-EG")).toBe("rtl");
      expect(directionOf(null, "AR_eg")).toBe("rtl");
      expect(directionOf(null, " he-IL ")).toBe("rtl");
      // A right-to-left LANGUAGE is not a right-to-left string match: "arn" (Mapudungun) and
      // "urj" begin with the letters of two RTL tags and are written left to right.
      expect(directionOf(null, "arn")).toBe("ltr");
      expect(directionOf(null, "urj-x")).toBe("ltr");
    });

    // Nothing known is not an error; it is the ordinary case for a document that says nothing
    // and a host with no navigator at all.
    it("reads left to right when it has nothing to go on", () => {
      expect(directionOf(null, null)).toBe("ltr");
      expect(directionOf(undefined, undefined)).toBe("ltr");
      expect(directionOf("", "")).toBe("ltr");
      expect(directionOf(null, "zz")).toBe("ltr");
    });
  });

  describe("which side the gutter opens on", () => {
    // A named side is an instruction, so the reading direction does not get a vote. Both sides
    // are asserted against both directions: "left" surviving an ltr document would prove
    // nothing on its own, because that is also what a mirrored "auto" would produce.
    it("obeys a named side under either direction", () => {
      expect(resolveGutter("left", "ltr")).toBe("left");
      expect(resolveGutter("left", "rtl")).toBe("left");
      expect(resolveGutter("right", "ltr")).toBe("right");
      expect(resolveGutter("right", "rtl")).toBe("right");
    });

    // "auto" is the START of the reading direction: the corner layout order begins at, and
    // the one the panel floats in when it is not pinned.
    it("mirrors with the reading direction when it is automatic", () => {
      expect(resolveGutter("auto", "ltr")).toBe("left");
      expect(resolveGutter("auto", "rtl")).toBe("right");
    });

    // The SAME rule as the floating anchor, asserted as an identity rather than as two lists
    // of equal answers. Two rules that agree today are two rules that can drift, and the one
    // thing this must not do is put the floating panel opposite the gutter "auto" would open.
    it("gives the automatic gutter and the floating anchor the same answer", () => {
      for (const dir of ["ltr", "rtl"] as const) {
        expect(resolveGutter("auto", dir)).toBe(startSide(dir));
      }
      expect(startSide("ltr")).toBe("left");
      expect(startSide("rtl")).toBe("right");
    });
  });
});
