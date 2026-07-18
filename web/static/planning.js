(() => {
  "use strict";

  const calendar = document.querySelector("[data-calendar-grid]");
  if (calendar) {
    calendar.addEventListener("keydown", (event) => {
      if (!/^Arrow(Left|Right|Up|Down)$/.test(event.key)) return;
      const cells = Array.from(calendar.querySelectorAll("[data-calendar-day]"));
      const current = cells.indexOf(document.activeElement);
      if (current < 0) return;
      const offset = { ArrowLeft: -1, ArrowRight: 1, ArrowUp: -7, ArrowDown: 7 }[event.key];
      const next = cells[current + offset];
      if (!next) return;
      event.preventDefault();
      next.focus();
    });
  }

  const timezone = document.querySelector("[data-timezone-input]");
  if (timezone && !timezone.value && typeof Intl !== "undefined") {
    const detected = Intl.DateTimeFormat().resolvedOptions().timeZone;
    if (detected) {
      timezone.value = detected;
      const note = document.querySelector("[data-timezone-detected]");
      if (note) note.hidden = false;
    }
  }
})();
