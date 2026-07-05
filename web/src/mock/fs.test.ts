import { describe, expect, it } from "vitest";
import { handleFs } from "./fs";

// The mock is a fixture for the SPA's tests, so anything it permits that the BFF refuses
// gets pinned as working behaviour by every test that runs against it. The read-only gate
// was honoured by the batch transfer and by nothing else — so a single-item copy, an
// upload, a rename or a delete into a `:ro` bind all "succeeded" here while the Go server
// answers 403.
describe("mock fs read-only gate", () => {
  const call = (method: string, rel: string, query: string, body = "") =>
    handleFs(method, rel, new URL(`http://x${rel}?${query}`), body);

  const cases: Array<[string, string, string, string]> = [
    ["PUT", "/fs/content", "source=virtual&path=seed/seed.sql", "clobbered"],
    ["POST", "/fs/upload", "source=virtual&path=seed&name=new.txt", "x"],
    ["POST", "/fs/mkdir", "source=virtual&path=seed/sub", ""],
    ["POST", "/fs/rename", "source=virtual&path=seed/seed.sql", `{"to":"seed/moved.md"}`],
    ["DELETE", "/fs", "source=virtual&path=seed/seed.sql", ""],
    ["POST", "/fs/copy", "source=virtual&path=project/compose.yaml", `{"to":"seed/copy.yaml"}`],
    ["POST", "/fs/move", "source=virtual&path=project/compose.yaml", `{"to":"seed/moved.yaml"}`],
  ];

  for (const [method, rel, query, body] of cases) {
    it(`refuses ${method} ${rel}`, () => {
      expect(call(method, rel, query, body).status).toBe(403);
    });
  }

  // The paired positive: the gate is about the DESTINATION, so reading out of a read-only
  // mount and writing somewhere else is exactly what a read-only mount is for. Without
  // this the tests above would still pass if the mock simply refused everything.
  it("allows a copy OUT of a read-only mount", () => {
    const res = call("POST", "/fs/copy", "source=virtual&path=seed/seed.sql", `{"to":"project/from-seed.sql"}`);
    expect(res.status).toBe(200);
  });
});
