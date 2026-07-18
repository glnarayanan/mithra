(function (global) {
  "use strict";

  function setStatus(target, message) {
    if (!target) {
      return;
    }
    target.textContent = message == null ? "" : String(message);
  }

  function install(root) {
    if (!root || typeof root.querySelector !== "function") {
      return;
    }
    var target = root.querySelector("[data-status]");
    if (typeof root.addEventListener === "function") {
      root.addEventListener("mithra:status", function (event) {
        setStatus(target, event.detail);
      });
    }
  }

  var api = Object.freeze({ install: install, setStatus: setStatus });

  if (typeof module !== "undefined" && module.exports) {
    module.exports = api;
  }
  if (global && global.document) {
    global.Mithra = api;
    global.document.addEventListener("DOMContentLoaded", function () {
      install(global.document);
    });
  }
})(typeof globalThis === "undefined" ? null : globalThis);
