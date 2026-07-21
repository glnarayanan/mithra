import assert from "node:assert/strict";
import test from "node:test";
import brief from "./brief.js";

test("install ignores missing document capabilities", () => {
  assert.doesNotThrow(() => brief.install(null));
  assert.doesNotThrow(() => brief.install({}));
});

test("refresh status uses text content", () => {
  let submit;
  const status = { textContent: "", dataset: {} };
  const form = { addEventListener: (_name, callback) => { submit = callback; } };
  brief.install({ querySelector: (selector) => selector === "[data-coaching-refresh]" ? form : status });
  submit();
  assert.match(status.textContent, /records you can see/);
  assert.equal(status.dataset.tone, "working");
});
