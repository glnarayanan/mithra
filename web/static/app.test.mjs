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

test("quick navigation reads the server navigation and filters labels", () => {
  const links = [
    { textContent: "Family Brief", getAttribute: () => "/" },
    { textContent: "Week in Review", getAttribute: () => "/review" },
    { textContent: "Family Brief", getAttribute: () => "/" },
  ];
  const destinations = app.navigationDestinations({ querySelectorAll: () => links });

  assert.deepEqual(destinations, [
    { label: "Family Brief", path: "/" },
    { label: "Week in Review", path: "/review" },
  ]);
  assert.deepEqual(app.filterQuickNavigation(destinations, "review").map(({ label }) => label), ["Week in Review"]);
  assert.deepEqual(app.filterQuickNavigation(destinations, " "), destinations);
});

test("keyboard help shortcut ignores editing, composition, repeats, and open dialogs", () => {
  const shortcut = (overrides = {}) => ({ key: "?", target: null, ...overrides });
  const noModal = { querySelector: () => null };
  const input = { tagName: "INPUT", parentElement: null };

  assert.equal(app.shouldOpenShortcutHelp(shortcut(), noModal, null), true);
  assert.equal(app.shouldOpenShortcutHelp(shortcut({ shiftKey: true }), noModal, null), true);
  assert.equal(app.shouldOpenShortcutHelp(shortcut({ target: input }), noModal, null), false);
  assert.equal(app.shouldOpenShortcutHelp(shortcut({ isComposing: true }), noModal, null), false);
  assert.equal(app.shouldOpenShortcutHelp(shortcut({ repeat: true }), noModal, null), false);
  assert.equal(app.shouldOpenShortcutHelp(shortcut(), { querySelector: () => ({}) }, null), false);
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

test("action shortcuts navigate without stealing keystrokes from editing or dialogs", () => {
  const noModal = { querySelector: () => null };
  const event = (key, overrides = {}) => ({ key, shiftKey: true, target: null, ...overrides });
  const input = { tagName: "INPUT", parentElement: null };

  assert.equal(app.actionShortcutPath(event("i"), noModal), "/imports");
  assert.equal(app.actionShortcutPath(event("p"), noModal), "/planning");
  assert.equal(app.actionShortcutPath(event("i", { target: input }), noModal), "");
  assert.equal(app.actionShortcutPath(event("i", { isComposing: true }), noModal), "");
  assert.equal(app.actionShortcutPath(event("i"), { querySelector: () => ({}) }), "");
  assert.equal(app.actionShortcutPath(event("x"), noModal), "");
});

test("Q opens quick capture without stealing ordinary typing", () => {
  const noModal = { querySelector: () => null };
  const input = { tagName: "INPUT", parentElement: null };
  const event = (overrides = {}) => ({ key: "q", target: null, ...overrides });

  assert.equal(app.shouldOpenQuickCapture(event(), noModal, null), true);
  assert.equal(app.shouldOpenQuickCapture(event({ key: "Q" }), noModal, null), true);
  assert.equal(app.shouldOpenQuickCapture(event({ target: input }), noModal, null), false);
  assert.equal(app.shouldOpenQuickCapture(event({ metaKey: true }), noModal, null), false);
  assert.equal(app.shouldOpenQuickCapture(event({ repeat: true }), noModal, null), false);
  assert.equal(app.shouldOpenQuickCapture(event(), { querySelector: () => ({}) }, null), false);
});

test("page errors log only safe diagnostic fields", () => {
  const calls = [];
  const element = { getAttribute: (name) => name === "data-error-code" ? "import_ai_rate_limited" : "request-123" };
  app.reportPageErrors({ querySelectorAll: () => [element] }, { error: (...args) => calls.push(args) });
  assert.deepEqual(calls, [["Mithra request failed", { code: "import_ai_rate_limited", reference: "request-123" }]]);
});
