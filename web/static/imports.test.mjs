import assert from "node:assert/strict";
import test from "node:test";
import imports from "./imports.js";

test("validateFile accepts supported bounded files", () => {
  assert.equal(imports.validateFile({ name: "family.csv", size: 128 }), "");
  assert.equal(imports.validateFile({ name: "report.XLSX", size: 10 * 1024 * 1024 }), "");
});

test("validateFile rejects unsupported, empty, and oversized files", () => {
  assert.match(imports.validateFile({ name: "notes.txt", size: 10 }), /CSV/);
  assert.match(imports.validateFile({ name: "report.pdf", size: 0 }), /10 MB/);
  assert.match(imports.validateFile({ name: "report.pdf", size: 10 * 1024 * 1024 + 1 }), /10 MB/);
});

test("setMessage treats file names as text", () => {
  const target = { textContent: "", dataset: {} };
  imports.setMessage(target, "<img onerror=alert(1)>", "error");
  assert.equal(target.textContent, "<img onerror=alert(1)>");
  assert.equal(target.dataset.tone, "error");
});

test("changing a corrected field clears its stale server error", () => {
  const attributes = { "aria-invalid": "true", "aria-describedby": "error-0-date" };
  const error = { hidden: false };
  const field = {
    getAttribute: (name) => attributes[name] || null,
    removeAttribute: (name) => { delete attributes[name]; },
  };

  imports.clearFieldError(field, { getElementById: () => error });

  assert.equal(error.hidden, true);
  assert.equal(field.getAttribute("aria-invalid"), null);
  assert.equal(field.getAttribute("aria-describedby"), null);
});
