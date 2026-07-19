(function (global) {
  "use strict";

  var quickNavigationDestinations = Object.freeze([
    Object.freeze({ label: "Family Brief", path: "/" }),
    Object.freeze({ label: "Week in Review", path: "/review" }),
    Object.freeze({ label: "Capture", path: "/capture" }),
    Object.freeze({ label: "Import", path: "/imports" }),
    Object.freeze({ label: "Finance", path: "/finance" }),
    Object.freeze({ label: "Health", path: "/health" }),
    Object.freeze({ label: "Planning", path: "/planning" }),
    Object.freeze({ label: "Settings", path: "/settings" }),
    Object.freeze({ label: "Help", path: "/help" })
  ]);

  function setStatus(target, message) {
    if (!target) {
      return;
    }
    target.textContent = message == null ? "" : String(message);
  }

  function filterQuickNavigation(query) {
    var needle = String(query || "").trim().toLowerCase();
    return quickNavigationDestinations.filter(function (destination) {
      return destination.label.toLowerCase().indexOf(needle) !== -1;
    });
  }

  function isEditableControl(target) {
    for (var element = target; element; element = element.parentElement) {
      var tagName = String(element.tagName || "").toLowerCase();
      if (tagName === "input" || tagName === "textarea" || tagName === "select" || element.isContentEditable) {
        return true;
      }
      if (typeof element.getAttribute === "function" && element.getAttribute("contenteditable") !== null && element.getAttribute("contenteditable") !== "false") {
        return true;
      }
    }
    return false;
  }

  function shouldOpenQuickNavigation(event, documentRoot, ownedDialog) {
    if (!event || event.defaultPrevented || event.repeat || event.isComposing || event.keyCode === 229 || event.altKey || (!event.ctrlKey && !event.metaKey) || String(event.key || "").toLowerCase() !== "k" || isEditableControl(event.target)) {
      return false;
    }
    var modal = documentRoot && typeof documentRoot.querySelector === "function" ? documentRoot.querySelector("dialog[open], [aria-modal=\"true\"]") : null;
    return !modal || modal === ownedDialog;
  }

  function restoreFocus(previousFocus, fallback) {
    var target = previousFocus && previousFocus.isConnected !== false && typeof previousFocus.focus === "function" ? previousFocus : fallback;
    if (target && typeof target.focus === "function") {
      target.focus();
    }
  }

  function installQuickNavigation(root) {
    if (!root || typeof root.querySelector !== "function" || typeof root.createElement !== "function" || root.querySelector("[data-quick-navigation-trigger]")) {
      return;
    }
    var header = root.querySelector(".header-content");
    if (!header) {
      return;
    }

    var trigger = root.createElement("button");
    trigger.type = "button";
    trigger.className = "quick-navigation-trigger";
    trigger.setAttribute("data-quick-navigation-trigger", "");
    trigger.setAttribute("aria-haspopup", "dialog");
    trigger.setAttribute("aria-controls", "quick-navigation");
    trigger.setAttribute("aria-keyshortcuts", "Control+K Meta+K");
    trigger.textContent = "Quick navigation";

    var dialog = root.createElement("dialog");
    dialog.id = "quick-navigation";
    dialog.className = "quick-navigation-dialog";
    dialog.setAttribute("aria-labelledby", "quick-navigation-title");
    var panel = root.createElement("div");
    panel.className = "quick-navigation-panel";
    var heading = root.createElement("h2");
    heading.id = "quick-navigation-title";
    heading.textContent = "Quick navigation";
    var close = root.createElement("button");
    close.type = "button";
    close.className = "quick-navigation-close";
    close.setAttribute("aria-label", "Close quick navigation");
    close.textContent = "Close";
    var search = root.createElement("input");
    search.type = "search";
    search.className = "quick-navigation-search";
    search.setAttribute("aria-label", "Filter destinations");
    search.setAttribute("aria-controls", "quick-navigation-results");
    search.setAttribute("autocomplete", "off");
    search.setAttribute("placeholder", "Filter destinations");
    var results = root.createElement("ul");
    results.id = "quick-navigation-results";
    results.className = "quick-navigation-results";
    results.setAttribute("role", "listbox");
    panel.appendChild(heading);
    panel.appendChild(close);
    panel.appendChild(search);
    panel.appendChild(results);
    dialog.appendChild(panel);
    header.appendChild(trigger);
    root.body.appendChild(dialog);

    var filtered = quickNavigationDestinations.slice();
    var activeIndex = 0;
    var previousFocus = trigger;

    function navigate(destination) {
      if (destination && global.location && typeof global.location.assign === "function") {
        global.location.assign(destination.path);
      }
    }

    function renderResults() {
      while (results.firstChild) {
        results.removeChild(results.firstChild);
      }
      if (activeIndex >= filtered.length) {
        activeIndex = 0;
      }
      filtered.forEach(function (destination, index) {
        var item = root.createElement("li");
        var link = root.createElement("a");
        link.href = destination.path;
        link.textContent = destination.label;
        link.id = "quick-navigation-option-" + index;
        link.setAttribute("role", "option");
        link.setAttribute("aria-selected", index === activeIndex ? "true" : "false");
        if (index === activeIndex) {
          link.className = "is-active";
          search.setAttribute("aria-activedescendant", link.id);
        }
        item.appendChild(link);
        results.appendChild(item);
      });
      if (!filtered.length) {
        search.removeAttribute("aria-activedescendant");
        var empty = root.createElement("li");
        empty.className = "quick-navigation-empty";
        empty.textContent = "No destinations found.";
        results.appendChild(empty);
      }
    }

    function closePalette() {
      if (dialog.open && typeof dialog.close === "function") {
        dialog.close();
      }
    }

    function openPalette() {
      if (dialog.open || typeof dialog.showModal !== "function") {
        return;
      }
      filtered = quickNavigationDestinations.slice();
      activeIndex = 0;
      previousFocus = root.activeElement;
      search.value = "";
      renderResults();
      dialog.showModal();
      search.focus();
    }

    trigger.addEventListener("click", openPalette);
    close.addEventListener("click", closePalette);
    search.addEventListener("input", function () {
      filtered = filterQuickNavigation(search.value);
      activeIndex = 0;
      renderResults();
    });
    dialog.addEventListener("cancel", function (event) {
      event.preventDefault();
      closePalette();
    });
    dialog.addEventListener("close", function () {
      restoreFocus(previousFocus, trigger);
    });
    dialog.addEventListener("keydown", function (event) {
      if (event.key === "Escape") {
        event.preventDefault();
        closePalette();
        return;
      }
      if (event.target === search && (event.key === "ArrowDown" || event.key === "ArrowUp")) {
        if (!filtered.length) {
          return;
        }
        event.preventDefault();
        activeIndex = (activeIndex + (event.key === "ArrowDown" ? 1 : filtered.length - 1)) % filtered.length;
        renderResults();
        return;
      }
      if (event.target === search && event.key === "Enter" && filtered[activeIndex]) {
        event.preventDefault();
        navigate(filtered[activeIndex]);
        return;
      }
      if (event.key === "Tab") {
        var focusable = dialog.querySelectorAll("button:not([disabled]), input:not([disabled]), [href]");
        if (!focusable.length) {
          return;
        }
        var first = focusable[0];
        var last = focusable[focusable.length - 1];
        if (event.shiftKey && root.activeElement === first) {
          event.preventDefault();
          last.focus();
        } else if (!event.shiftKey && root.activeElement === last) {
          event.preventDefault();
          first.focus();
        }
      }
    });
    root.addEventListener("keydown", function (event) {
      if (shouldOpenQuickNavigation(event, root, dialog) && !dialog.open) {
        event.preventDefault();
        openPalette();
      }
    });
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
    installQuickNavigation(root);
  }

  var api = Object.freeze({
    destinations: quickNavigationDestinations,
    filterQuickNavigation: filterQuickNavigation,
    install: install,
    isEditableControl: isEditableControl,
    restoreFocus: restoreFocus,
    setStatus: setStatus,
    shouldOpenQuickNavigation: shouldOpenQuickNavigation
  });

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
