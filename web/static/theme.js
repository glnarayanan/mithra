// Applies the saved Day or Night choice before first paint. Auto follows the system.
(function () {
  "use strict";
  try {
    var choice = window.localStorage.getItem("mithra-theme");
    if (choice === "day" || choice === "night") document.documentElement.setAttribute("data-theme", choice);
  } catch (error) { /* storage unavailable: follow the system theme */ }
})();
