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

test("quick navigation has only the approved destinations and filters labels", () => {
  assert.deepEqual(app.destinations.map(({ label, path }) => [label, path]), [
    ["Family Brief", "/"], ["Week in Review", "/review"], ["Capture", "/capture"], ["Import", "/imports"], ["Finance", "/finance"], ["Health", "/health"], ["Planning", "/planning"], ["Settings", "/settings"], ["Help", "/help"]
  ]);
  assert.deepEqual(app.filterQuickNavigation("review").map(({ label }) => label), ["Week in Review"]);
  assert.deepEqual(app.filterQuickNavigation(" ").map(({ label }) => label), app.destinations.map(({ label }) => label));
});

test("quick navigation shortcut respects ordinary editing and modal use", () => {
  const shortcut = (overrides = {}) => ({ key: "k", ctrlKey: true, target: null, ...overrides });
  const noModal = { querySelector: () => null };
  const input = { tagName: "INPUT", parentElement: null };
  const textarea = { tagName: "TEXTAREA", parentElement: null };
  const select = { tagName: "SELECT", parentElement: null };
  const editable = { tagName: "DIV", parentElement: null, getAttribute: (name) => name === "contenteditable" ? "" : null };
  const composing = shortcut({ isComposing: true });
  const repeated = shortcut({ repeat: true });
  const editing = shortcut({ target: input });
  const otherModal = {};
  const activeModal = { querySelector: () => otherModal };

  assert.equal(app.shouldOpenQuickNavigation(shortcut(), noModal, null), true);
  assert.equal(app.shouldOpenQuickNavigation(shortcut({ metaKey: true, ctrlKey: false }), noModal, null), true);
  assert.equal(app.shouldOpenQuickNavigation(composing, noModal, null), false);
  assert.equal(app.shouldOpenQuickNavigation(repeated, noModal, null), false);
  assert.equal(app.shouldOpenQuickNavigation(editing, noModal, null), false);
  assert.equal(app.shouldOpenQuickNavigation(shortcut({ target: textarea }), noModal, null), false);
  assert.equal(app.shouldOpenQuickNavigation(shortcut({ target: select }), noModal, null), false);
  assert.equal(app.shouldOpenQuickNavigation(shortcut({ target: editable }), noModal, null), false);
  assert.equal(app.shouldOpenQuickNavigation(shortcut(), activeModal, null), false);
  assert.equal(app.shouldOpenQuickNavigation(shortcut(), activeModal, otherModal), true);
});

test("quick navigation restores the focused invoker with a safe fallback", () => {
  let focused = "";
  const invoker = { isConnected: true, focus: () => { focused = "invoker"; } };
  const fallback = { focus: () => { focused = "fallback"; } };

  app.restoreFocus(invoker, fallback);
  assert.equal(focused, "invoker");
  app.restoreFocus({ isConnected: false, focus: () => { focused = "stale"; } }, fallback);
  assert.equal(focused, "fallback");
});
