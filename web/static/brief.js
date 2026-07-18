(function (global) {
  "use strict";

  function install(root) {
    if (!root || typeof root.querySelector !== "function") return;
    var form = root.querySelector("[data-coaching-refresh]");
    var status = root.querySelector("[data-coaching-status]");
    if (!form || !status || typeof form.addEventListener !== "function") return;
    form.addEventListener("submit", function () {
      status.textContent = "Rechecking the household facts before asking Mithra for a fresh view…";
      status.dataset.tone = "working";
    });
  }

  var api = Object.freeze({ install: install });
  if (typeof module !== "undefined" && module.exports) module.exports = api;
  if (global && global.document) global.document.addEventListener("DOMContentLoaded", function () { install(global.document); });
})(typeof globalThis === "undefined" ? null : globalThis);
