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

function eventTarget(properties = {}) {
  const listeners = new Map();
  return Object.assign(properties, {
    addEventListener(type, listener) {
      const current = listeners.get(type) || [];
      current.push(listener);
      listeners.set(type, current);
    },
    async emit(type, event = {}) {
      for (const listener of listeners.get(type) || []) await listener(event);
    },
  });
}

function voiceFixture(getUserMedia) {
  const record = eventTarget({ disabled: false });
  const stop = eventTarget({ hidden: true });
  const cancel = eventTarget({ hidden: true });
  const status = { textContent: "", dataset: {} };
  const dialog = eventTarget({ open: true });
  const csrf = { value: "csrf-token" };
  const visibility = { value: "shared" };
  const composer = { querySelector: (selector) => selector === "[name=csrf]" ? csrf : visibility };
  const controls = { "[data-record]": record, "[data-stop]": stop, "[data-cancel]": cancel, "[data-voice-status]": status };
  const panel = {
    querySelector: (selector) => controls[selector] || null,
    closest: (selector) => selector === "dialog" ? dialog : selector === "[data-capture-composer]" ? composer : null,
  };
  let recorder;
  class MediaRecorder {
    static isTypeSupported(type) { return type === "audio/webm"; }
    constructor() {
      this.state = "inactive";
      this.listeners = new Map();
      recorder = this;
    }
    addEventListener(type, listener) { this.listeners.set(type, listener); }
    start() { this.state = "recording"; }
    stop() {
      this.state = "inactive";
      const listener = this.listeners.get("stop");
      if (listener) listener();
    }
    emit(type, event) {
      const listener = this.listeners.get(type);
      if (listener) listener(event);
    }
  }
  const uploads = [];
  const environment = {
    navigator: { mediaDevices: { getUserMedia } },
    MediaRecorder,
    Blob,
    FormData,
    fetch: (...args) => { uploads.push(args); return Promise.resolve({ ok: true, url: "/capture" }); },
    location: { assign() {} },
    setTimeout,
    clearTimeout,
  };
  capture.installPanel(panel, environment);
  return { record, dialog, get recorder() { return recorder; }, uploads };
}

test("closing quick capture discards an active recording", async () => {
  let stopped = 0;
  const stream = { getTracks: () => [{ stop: () => { stopped += 1; } }] };
  const fixture = voiceFixture(async () => stream);

  await fixture.record.emit("click");
  fixture.recorder.emit("dataavailable", { data: new Blob(["audio"]) });
  fixture.dialog.open = false;
  await fixture.dialog.emit("close");

  assert.equal(stopped, 1);
  assert.equal(fixture.uploads.length, 0);
});

test("closing while microphone permission is pending stops the late stream", async () => {
  let resolvePermission;
  let stopped = 0;
  const permission = new Promise((resolve) => { resolvePermission = resolve; });
  const fixture = voiceFixture(() => permission);
  const recording = fixture.record.emit("click");

  fixture.dialog.open = false;
  await fixture.dialog.emit("close");
  resolvePermission({ getTracks: () => [{ stop: () => { stopped += 1; } }] });
  await recording;

  assert.equal(stopped, 1);
  assert.equal(fixture.recorder, undefined);
  assert.equal(fixture.uploads.length, 0);
});
