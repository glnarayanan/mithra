import assert from "node:assert/strict";
import test from "node:test";
import app from "./app.js";

test("setStatus preserves untrusted strings as text", () => {
  const target = { textContent: "" };
  const untrusted = "<img src=x onerror=window.pwned()>";

  app.setStatus(target, untrusted);

  assert.equal(target.textContent, untrusted);
});

test("install ignores missing document capabilities", () => {
  assert.doesNotThrow(() => app.install(null));
  assert.doesNotThrow(() => app.install({}));
});
