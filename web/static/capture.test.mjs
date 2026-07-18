import assert from "node:assert/strict";
import test from "node:test";
import capture from "./capture.js";

test("voice limits reject empty, oversize, and overlong recordings", () => {
  assert.equal(capture.withinLimits(1, 1), true);
  assert.equal(capture.withinLimits(0, 1), false);
  assert.equal(capture.withinLimits(8 * 1024 * 1024 + 1, 1), false);
  assert.equal(capture.withinLimits(1, 91), false);
});

test("voice format chooses the first supported safe option", () => {
  const MediaRecorder = { isTypeSupported: (type) => type === "audio/webm" };
  assert.equal(capture.supportedType(MediaRecorder), "audio/webm");
  assert.equal(capture.supportedType(null), "");
});

test("messages preserve untrusted content as text", () => {
  const target = { textContent: "", dataset: {} };
  capture.setMessage(target, "<img src=x onerror=alert(1)>", "error");
  assert.equal(target.textContent, "<img src=x onerror=alert(1)>");
  assert.equal(target.dataset.tone, "error");
});
