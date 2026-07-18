(function (global) {
  "use strict";

  var maximumBytes = 10 * 1024 * 1024;
  var allowedExtensions = ["csv", "xlsx", "pdf"];

  function validateFile(file) {
    if (!file || typeof file.name !== "string" || !Number.isFinite(file.size)) return "Choose one CSV, XLSX, or PDF file.";
    var parts = file.name.toLowerCase().split(".");
    var extension = parts.length > 1 ? parts.pop() : "";
    if (allowedExtensions.indexOf(extension) < 0) return "Choose a CSV, XLSX, or PDF file.";
    if (file.size < 1 || file.size > maximumBytes) return "Choose a file between 1 byte and 10 MB.";
    return "";
  }

  function setMessage(target, message, tone) {
    if (!target) return;
    target.textContent = message;
    target.dataset.tone = tone || "quiet";
  }

  function install(root) {
    if (!root || typeof root.querySelector !== "function") return;
    var form = root.querySelector("[data-import-upload]");
    if (form) {
      var input = form.querySelector("[data-import-file]");
      var status = form.querySelector("[data-import-status]");
      if (input && typeof input.addEventListener === "function") input.addEventListener("change", function () {
        var file = input.files && input.files[0];
        var problem = validateFile(file);
        input.setCustomValidity(problem);
        setMessage(status, problem || (file.name + " is ready to read locally."), problem ? "error" : "quiet");
      });
      if (typeof form.addEventListener === "function") form.addEventListener("submit", function () {
        var file = input && input.files && input.files[0];
        var problem = validateFile(file);
        if (input) input.setCustomValidity(problem);
        if (problem) {
          setMessage(status, problem, "error");
          if (input && typeof input.reportValidity === "function") input.reportValidity();
          return;
        }
        setMessage(status, "Reading the file locally and preparing a review…", "working");
      });
    }
    var blocker = root.querySelector("[data-first-blocker]");
    if (blocker && typeof blocker.focus === "function") blocker.focus();
  }

  var api = Object.freeze({ install: install, setMessage: setMessage, validateFile: validateFile });
  if (typeof module !== "undefined" && module.exports) module.exports = api;
  if (global && global.document) global.document.addEventListener("DOMContentLoaded", function () { install(global.document); });
})(typeof globalThis === "undefined" ? null : globalThis);
