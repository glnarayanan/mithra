(function (global) {
  "use strict";

  var maximumBytes = 8 * 1024 * 1024;
  var maximumDurationSeconds = 90;

  function supportedType(MediaRecorderClass) {
    if (!MediaRecorderClass || typeof MediaRecorderClass.isTypeSupported !== "function") return "";
    var candidates = ["audio/webm;codecs=opus", "audio/webm", "audio/ogg;codecs=opus", "audio/mp4"];
    return candidates.find(function (type) { return MediaRecorderClass.isTypeSupported(type); }) || "";
  }

  function withinLimits(bytes, durationSeconds) {
    return bytes > 0 && bytes <= maximumBytes && durationSeconds > 0 && durationSeconds <= maximumDurationSeconds;
  }

  function setMessage(target, message, tone) {
    if (!target) return;
    target.textContent = message;
    target.dataset.tone = tone || "quiet";
  }

  function install(root, environment) {
    if (!root || typeof root.querySelector !== "function") return;
    var panel = root.querySelector("[data-voice-capture]");
    if (!panel) return;
    environment = environment || global;
    var navigatorObject = environment && environment.navigator;
    var MediaRecorderClass = environment && environment.MediaRecorder;
    var record = panel.querySelector("[data-record]");
    var stop = panel.querySelector("[data-stop]");
    var cancel = panel.querySelector("[data-cancel]");
    var status = panel.querySelector("[data-voice-status]");
    var csrf = panel.querySelector("[name=csrf]");
    var visibility = panel.querySelector("[name=visibility]");
    var type = supportedType(MediaRecorderClass);
    if (!record || !stop || !cancel || !navigatorObject || !navigatorObject.mediaDevices || typeof navigatorObject.mediaDevices.getUserMedia !== "function" || !type) {
      if (record) record.disabled = true;
      setMessage(status, "Voice capture is not supported in this browser. You can still type an update.", "quiet");
      return;
    }

    var recorder = null;
    var stream = null;
    var chunks = [];
    var startedAt = 0;
    var timer = 0;

    function finishStream() {
      if (timer) environment.clearTimeout(timer);
      timer = 0;
      if (stream) stream.getTracks().forEach(function (track) { track.stop(); });
      stream = null;
      record.disabled = false;
      stop.hidden = true;
      cancel.hidden = true;
    }

    function discard() {
      chunks = [];
      if (recorder && recorder.state !== "inactive") recorder.stop();
      recorder = null;
      finishStream();
      setMessage(status, "Recording discarded. Nothing was saved or sent.", "quiet");
    }

    async function upload(blob, durationSeconds) {
      if (!withinLimits(blob.size, durationSeconds)) {
        setMessage(status, "That recording is empty, longer than 90 seconds, or larger than 8 MB. Nothing was sent.", "error");
        return;
      }
      var form = new environment.FormData();
      form.append("csrf", csrf ? csrf.value : "");
      form.append("visibility", visibility ? visibility.value : "personal");
      form.append("duration_seconds", String(Math.ceil(durationSeconds)));
      form.append("audio", blob, "mithra-update." + (type.indexOf("ogg") >= 0 ? "ogg" : type.indexOf("mp4") >= 0 ? "mp4" : "webm"));
      setMessage(status, "Transcribing this update…", "working");
      try {
        var response = await environment.fetch("/capture/voice", { method: "POST", body: form, credentials: "same-origin" });
        if (!response.ok) throw new Error("upload failed");
        environment.location.assign(response.url || "/capture");
      } catch (_) {
        setMessage(status, "The recording could not be processed. Try once more or type the update instead.", "error");
      }
    }

    record.addEventListener("click", async function () {
      record.disabled = true;
      setMessage(status, "Waiting for microphone permission…", "working");
      try {
        stream = await navigatorObject.mediaDevices.getUserMedia({ audio: true });
        chunks = [];
        recorder = new MediaRecorderClass(stream, { mimeType: type });
        recorder.addEventListener("dataavailable", function (event) { if (event.data && event.data.size) chunks.push(event.data); });
        recorder.addEventListener("stop", function () {
          var duration = (Date.now() - startedAt) / 1000;
          var blob = new environment.Blob(chunks, { type: type });
          chunks = [];
          finishStream();
          if (recorder) upload(blob, duration);
          recorder = null;
        });
        recorder.start(1000);
        startedAt = Date.now();
        stop.hidden = false;
        cancel.hidden = false;
        setMessage(status, "Recording. Stop within 90 seconds when you are done.", "working");
        timer = environment.setTimeout(function () { if (recorder && recorder.state !== "inactive") recorder.stop(); }, maximumDurationSeconds * 1000);
      } catch (_) {
        finishStream();
        setMessage(status, "Microphone access was not granted. Nothing was saved or sent.", "error");
      }
    });
    stop.addEventListener("click", function () { if (recorder && recorder.state !== "inactive") recorder.stop(); });
    cancel.addEventListener("click", discard);
  }

  var api = Object.freeze({ install: install, supportedType: supportedType, withinLimits: withinLimits, setMessage: setMessage });
  if (typeof module !== "undefined" && module.exports) module.exports = api;
  if (global && global.document) global.document.addEventListener("DOMContentLoaded", function () { install(global.document, global); });
})(typeof globalThis === "undefined" ? null : globalThis);
