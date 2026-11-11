// Baby log charts: daily rhythm, bottle intake, diapers and weight. The
// server counts everything and writes it to data-chart-data; this script
// draws each chart at the width it is shown and answers hover, touch and
// the arrow keys with the numbers for that day or that weight.
(() => {
  "use strict";

  const esc = (value) => String(value).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
  const plural = (n, one, many) => `${n} ${n === 1 ? one : many}`;
  const fixed = (n, digits) => Number(n).toFixed(digits);
  const signed = (value, digits = 0) => {
    const rounded = Number(value.toFixed(digits));
    if (rounded === 0) return (0).toFixed(digits);
    return `${rounded > 0 ? "+" : "−"}${Math.abs(rounded).toFixed(digits)}`;
  };
  const kg = (value) => String(Math.round(value * 1000) / 1000);

  // A transparent band that answers the pointer and the keys with a tooltip.
  // ax, ay is the mark the tooltip points at, in chart units.
  const hit = (x, y, w, h, title, lines, ax, ay) => `<rect class="hit" x="${x.toFixed(1)}" y="${y.toFixed(1)}" width="${Math.max(1, w).toFixed(1)}" height="${h.toFixed(1)}" data-ax="${(ax ?? x + w / 2).toFixed(1)}" data-ay="${(ay ?? y).toFixed(1)}" data-tip="${esc(title)}" data-lines="${esc(lines.join("\n"))}"/>`;

  const table = (caption, head, rows) => `<table class="visually-hidden"><caption>${esc(caption)}</caption><thead><tr>${head.map((c) => `<th scope="col">${esc(c)}</th>`).join("")}</tr></thead><tbody>${rows.map((row) => `<tr>${row.map((c, i) => (i ? `<td>${esc(c)}</td>` : `<th scope="row">${esc(c)}</th>`)).join("")}</tr>`).join("")}</tbody></table>`;

  // Short ranges name the weekday; longer ones the date, with the month on
  // the first row and on the first of each month.
  const dayLabel = (day, days, index) => (days.length <= 14 ? day.short : index === 0 || day.num === "1" ? day.mark : day.num);

  function perDay(data, test) {
    const counts = data.days.map(() => 0);
    data.events.forEach((e) => { if (test(e)) counts[e.d] += 1; });
    return counts;
  }

  // Columns per day with day labels under them. Labels thin out on narrow
  // slots so nothing collides.
  function bars(W, data, series, label, tip) {
    const days = data.days;
    const height = 180;
    const top = 22;
    const base = height - 26;
    const slot = W / days.length;
    const stacked = series.length > 1 && slot < 18;
    const bar1 = Math.max(2, Math.min(slot * 0.7, 14));
    const max = Math.max(1, ...(stacked ? days.map((_, i) => series.reduce((sum, s) => sum + s.values[i], 0)) : series.flatMap((s) => s.values)));
    const group = Math.min(36, slot * 0.7);
    const bar = group / series.length;
    const step = Math.max(1, Math.ceil(44 / slot));
    let marks = "";
    let hits = "";
    days.forEach((day, i) => {
      const tallest = stacked ? series.reduce((sum, s) => sum + s.values[i], 0) : Math.max(...series.map((s) => s.values[i]));
      hits += hit(i * slot, top - 14, slot, base - top + 14, day.long, tip(i), i * slot + slot / 2, tallest ? base - Math.max(3, ((base - top) * tallest) / max) : base);
      const x0 = i * slot + (slot - group) / 2;
        // Narrow slots stack the series in one bar; wide ones set them side by side.
      let stackTop = base;
      series.forEach((s, si) => {
        const value = s.values[i];
        const h = value ? Math.max(3, ((base - top) * value) / max) : 0;
        if (!h) return;
        if (stacked) {
          marks += `<rect class="cm ${s.cls}" x="${(i * slot + (slot - bar1) / 2).toFixed(1)}" y="${(stackTop - h).toFixed(1)}" width="${bar1.toFixed(1)}" height="${h.toFixed(1)}"/>`;
          stackTop -= h;
          return;
        }
        marks += `<rect class="cm ${s.cls}" x="${(x0 + si * bar).toFixed(1)}" y="${(base - h).toFixed(1)}" width="${Math.max(2, bar - 2).toFixed(1)}" height="${h.toFixed(1)}" rx="${Math.min(6, Math.max(2, bar / 4)).toFixed(1)}"/>`;
        if (days.length <= 7 && series.length === 1) marks += `<text class="chart-value" x="${(x0 + group / 2).toFixed(1)}" y="${(base - h - 6).toFixed(1)}" text-anchor="middle">${Math.round(value)}</text>`;
      });
      if (i % step === 0 || i === days.length - 1) marks += `<text class="chart-label" x="${(i * slot + slot / 2).toFixed(1)}" y="${height - 6}" text-anchor="middle">${esc(dayLabel(day, days, i))}</text>`;
    });
    return `<svg class="chart" viewBox="0 0 ${W} ${height}" width="${W}" height="${height}" role="img" aria-label="${esc(label)}">${hits}<line class="axis" x1="0" x2="${W}" y1="${base}" y2="${base}"/>${marks}</svg>`;
  }

  // The entry types' shapes: feed circle, diaper rounded square, spit-up drop.
  function shape(type, x, y, r) {
    if (type === "diaper") return `<rect class="cm diaper" x="${(x - r).toFixed(1)}" y="${(y - r).toFixed(1)}" width="${(r * 2).toFixed(1)}" height="${(r * 2).toFixed(1)}" rx="${(r * 0.5).toFixed(1)}"/>`;
    if (type === "spitup") return `<path class="cm spitup" d="M${x.toFixed(1)} ${(y - r * 1.25).toFixed(1)}C${(x + r * 0.9).toFixed(1)} ${(y - r * 0.2).toFixed(1)} ${(x + r).toFixed(1)} ${(y + r * 0.2).toFixed(1)} ${(x + r).toFixed(1)} ${(y + r * 0.25).toFixed(1)}A${r.toFixed(1)} ${r.toFixed(1)} 0 0 1 ${(x - r).toFixed(1)} ${(y + r * 0.25).toFixed(1)}C${(x - r).toFixed(1)} ${(y + r * 0.2).toFixed(1)} ${(x - r * 0.9).toFixed(1)} ${(y - r * 0.2).toFixed(1)} ${x.toFixed(1)} ${(y - r * 1.25).toFixed(1)}Z"/>`;
    return `<circle class="cm feed" cx="${x.toFixed(1)}" cy="${y.toFixed(1)}" r="${r}"/>`;
  }

  function rhythm(W, full) {
    // Past 30 rows (14 on a phone) the dots run together, so the chart keeps
    // the most recent days and says so.
    const cap = W < 520 ? 14 : 30;
    const skip = Math.max(0, full.days.length - cap);
    const data = { days: full.days.slice(skip), events: full.events.filter((e) => e.d >= skip).map((e) => ({ ...e, d: e.d - skip })) };
    const days = data.days;
    const n = days.length;
    const row = n > 31 ? 6 : n > 14 ? 9 : n > 7 ? 14 : 22;
    const left = 56;
    const height = n * row + 30;
    const feeds = perDay(data, (e) => e.t === "feed");
    const diapers = perDay(data, (e) => e.t === "diaper");
    const spits = perDay(data, (e) => e.t === "spitup");
    // Every fifth row is named, plus the first of a month; a name never sits
    // within two rows of another.
    const labelled = new Set();
    days.forEach((day, i) => { if (n <= 14 || day.num === "1") labelled.add(i); });
    days.forEach((day, i) => {
      if ((i % 5 === 0 || i === n - 1) && ![...labelled].some((j) => Math.abs(j - i) < 3)) labelled.add(i);
    });
    let grid = "";
    let hits = "";
    days.forEach((day, i) => {
      const y = i * row + row / 2;
      if (labelled.has(i)) grid += `<text class="chart-label" x="0" y="${(y + 4).toFixed(1)}">${esc(n <= 14 ? `${day.short} ${day.num}` : dayLabel(day, days, i))}</text>`;
      grid += `<line class="grid" x1="${left}" x2="${W}" y1="${y.toFixed(1)}" y2="${y.toFixed(1)}"/>`;
      hits += hit(left, i * row, W - left, row, day.long, [plural(feeds[i], "feed", "feeds"), plural(diapers[i], "diaper", "diapers"), plural(spits[i], "spit-up", "spit-ups")]);
    });
    const hours = [0, 6, 12, 18, 24].map((h) => {
      const x = left + ((W - left) * h) / 24;
      return `<text class="chart-label" x="${x.toFixed(1)}" y="${height - 6}" text-anchor="${h === 0 ? "start" : h === 24 ? "end" : "middle"}">${h === 24 ? "24h" : `${h}h`}</text>`;
    }).join("");
    const r = n > 31 ? 2.2 : n > 14 ? 3 : 4.5;
    const dots = data.events.map((e) => shape(e.t, left + ((W - left) * e.m) / 1440, e.d * row + row / 2, r)).join("");
    const capped = skip ? `<p class="chart-cap">The chart shows the last ${n} days. The figures above cover all ${full.days.length}.</p>` : "";
    return `<svg class="chart" viewBox="0 0 ${W} ${height}" width="${W}" height="${height}" role="img" aria-label="Feeds, diapers and spit-ups by time of day, one row per day">${hits}${grid}${hours}${dots}</svg>${capped}` +
      table("Entries per day", ["Day", "Feeds", "Diapers", "Spit-ups"], days.map((d, i) => [d.long, feeds[i], diapers[i], spits[i]]));
  }

  function bottle(W, data) {
    const ml = data.days.map(() => 0);
    const count = data.days.map(() => 0);
    data.events.forEach((e) => { if (e.ml) { ml[e.d] += e.ml; count[e.d] += 1; } });
    return bars(W, data, [{ cls: "feed", values: ml }], "Bottle intake per day, ml", (i) => [`${Math.round(ml[i])} ml`, plural(count[i], "bottle", "bottles")]) +
      table("Bottle intake in ml", ["Day", "ml", "Bottles"], data.days.map((d, i) => [d.long, Math.round(ml[i]), count[i]]));
  }

  function diapers(W, data) {
    const wet = perDay(data, (e) => e.t === "diaper" && (e.k === "wet" || e.k === "both"));
    const dirty = perDay(data, (e) => e.t === "diaper" && (e.k === "dirty" || e.k === "both"));
    const all = perDay(data, (e) => e.t === "diaper");
    return bars(W, data, [{ cls: "wet", values: wet }, { cls: "dirty", values: dirty }], "Wet and dirty diapers per day", (i) => [plural(all[i], "diaper", "diapers"), `${wet[i]} wet, ${dirty[i]} dirty`]) +
      table("Diapers per day", ["Day", "Wet", "Dirty"], data.days.map((d, i) => [d.long, wet[i], dirty[i]]));
  }

  // Weights on a time axis that spans the whole window, so the slope reads
  // the same as the figures above it.
  function weight(W, data, compact) {
    const entries = data.weights || [];
    if (!entries.length) return "";
    const height = compact ? 150 : 180;
    const left = 52;
    const values = entries.map((e) => e.kg);
    // Round ticks, so a weight can be read against 4.5 or 5.0 kg.
    const lo = Math.min(...values);
    const hi = Math.max(...values);
    const step = [0.05, 0.1, 0.2, 0.5, 1, 2, 5].find((s) => (hi - lo) / s <= 3) || 10;
    const min = Math.floor(lo / step) * step;
    const max = Math.max(Math.ceil(hi / step) * step, min + step);
    const ticks = [];
    for (let v = min; v <= max + step / 2; v += step) ticks.push(v);
    const span = Math.max(data.to - data.from, 1);
    const plotTop = 12;
    const plotBottom = height - 30;
    const x = (at) => left + 8 + ((at - data.from) / span) * (W - left - 16);
    const y = (v) => plotBottom - ((v - min) / Math.max(max - min, 0.001)) * (plotBottom - plotTop);
    const digits = step < 0.1 ? 2 : step < 1 ? 1 : 0;
    const grid = ticks.map((v, i) => `<line class="grid" x1="${left}" x2="${W}" y1="${y(v).toFixed(1)}" y2="${y(v).toFixed(1)}"/><text class="chart-label" x="0" y="${(y(v) + 4).toFixed(1)}">${fixed(v, digits)}${i === ticks.length - 1 ? " kg" : ""}</text>`).join("");
    const xs = entries.map((e) => x(e.at));
    const points = entries.map((e, i) => `${xs[i].toFixed(1)},${y(e.kg).toFixed(1)}`).join(" ");
    const dots = entries.map((e, i) => `<circle class="cm weight" cx="${xs[i].toFixed(1)}" cy="${y(e.kg).toFixed(1)}" r="${entries.length > 40 ? 3 : 4.5}"/>`).join("");
    // Each point owns the band halfway to its neighbours.
    const hits = entries.map((e, i) => {
      const from = i ? (xs[i - 1] + xs[i]) / 2 : left;
      const to = i < entries.length - 1 ? (xs[i] + xs[i + 1]) / 2 : W;
      const lines = [`${kg(e.kg)} kg`];
      if (i) {
        const daysBetween = (e.at - entries[i - 1].at) / 864e5;
        const grams = (e.kg - entries[i - 1].kg) * 1000;
        lines.push(`${signed(grams)} g since the weight before`);
        if (daysBetween >= 0.5) lines.push(`${signed(grams / daysBetween)} g a day over ${plural(Math.round(daysBetween), "day", "days")}`);
      }
      return hit(from, plotTop - 6, to - from, plotBottom - plotTop + 12, e.label, lines, xs[i], y(e.kg));
    }).join("");
    const edges = `<text class="chart-label" x="${left + 8}" y="${height - 6}">${esc(data.fromLabel)}</text><text class="chart-label" x="${W}" y="${height - 6}" text-anchor="end">${esc(data.toLabel)}</text>`;
    return `<svg class="chart" viewBox="0 0 ${W} ${height}" width="${W}" height="${height}" role="img" aria-label="Weight in kilograms, ${plural(entries.length, "weight", "weights")} from ${esc(data.fromLabel)} to ${esc(data.toLabel)}">${hits}${grid}<polyline class="line weight" points="${points}"/>${dots}${edges}</svg>` +
      table("Weights", ["Date", "kg"], entries.map((e) => [e.label, kg(e.kg)]));
  }

  const draw = { rhythm, bottle, diapers, weight };

  function render(wrap) {
    let data;
    try {
      data = JSON.parse(wrap.dataset.chartData || "{}");
    } catch {
      return;
    }
    const kind = wrap.dataset.chart;
    if (!draw[kind]) return; // server-drawn, such as the 24-hour strip
    const W = Math.max(260, Math.round(wrap.clientWidth || 600));
    wrap.innerHTML = draw[kind](W, data, wrap.hasAttribute("data-compact"));
    wrap.dataset.drawn = String(W);
  }

  // ---- Tooltips --------------------------------------------------------

  function tooltip(wrap) {
    let tip = wrap.querySelector(".chart-tip");
    if (!tip) {
      tip = document.createElement("div");
      tip.className = "chart-tip";
      tip.setAttribute("role", "status");
      tip.hidden = true;
      wrap.append(tip);
    }
    return tip;
  }

  function show(wrap, band) {
    wrap.querySelectorAll(".hit.on").forEach((n) => n.classList.remove("on"));
    band.classList.add("on");
    const tip = tooltip(wrap);
    tip.innerHTML = `<strong>${esc(band.dataset.tip)}</strong>${band.dataset.lines.split("\n").filter(Boolean).map((line) => `<span>${esc(line)}</span>`).join("")}`;
    tip.hidden = false;
    // Point at the mark: centred on it, above it when there is room,
    // otherwise below, so the mark itself stays in view.
    const box = wrap.getBoundingClientRect();
    const svg = wrap.querySelector("svg");
    const scale = svg ? svg.getBoundingClientRect().width / svg.viewBox.baseVal.width : 1;
    const ax = Number(band.dataset.ax) * scale;
    const ay = Number(band.dataset.ay) * scale;
    const width = tip.offsetWidth;
    const height = tip.offsetHeight;
    // Above may rise over the figures above the chart, never off the card.
    const roomAbove = Math.min(box.top - (wrap.closest(".chart-block, .card, .baby-hero, .baby-panel") || wrap).getBoundingClientRect().top, 96);
    // Over bars, float above the tallest bar so no bar or value label is
    // covered; elsewhere, just above the mark.
    const bars = wrap.dataset.chart === "bottle" || wrap.dataset.chart === "diapers";
    const ceiling = bars ? Math.min(...[...wrap.querySelectorAll(".hit")].map((n) => Number(n.dataset.ay))) * scale - 22 : ay - 12;
    if (ceiling - height >= -roomAbove) {
      tip.style.left = `${Math.min(Math.max(ax - width / 2, 0), Math.max(box.width - width, 0))}px`;
      tip.style.top = `${ceiling - height}px`;
    } else {
      // No room above: sit beside the mark, on the side with more room.
      const right = ax + 14 + width <= box.width || ax < box.width / 2;
      tip.style.left = `${right ? Math.min(ax + 14, Math.max(box.width - width, 0)) : Math.max(ax - 14 - width, 0)}px`;
      tip.style.top = `${Math.max(0, ay - 10)}px`;
    }
  }

  function hide(wrap) {
    wrap.querySelectorAll(".hit.on").forEach((n) => n.classList.remove("on"));
    const tip = wrap.querySelector(".chart-tip");
    if (tip) tip.hidden = true;
  }

  function wire(wrap) {
    const pick = (event) => {
      const band = event.target.closest && event.target.closest(".hit");
      if (band) show(wrap, band);
    };
    wrap.addEventListener("pointermove", pick);
    wrap.addEventListener("pointerdown", pick);
    wrap.addEventListener("pointerleave", (event) => { if (event.pointerType === "mouse") hide(wrap); });
    wrap.addEventListener("blur", () => hide(wrap));
    wrap.addEventListener("focus", () => {
      const bands = [...wrap.querySelectorAll(".hit")].sort((a, b) => Number(a.dataset.ax) - Number(b.dataset.ax));
      if (bands.length) show(wrap, bands[bands.length - 1]);
    });
    wrap.addEventListener("keydown", (event) => {
      const bands = [...wrap.querySelectorAll(".hit")].sort((a, b) => Number(a.dataset.ax) - Number(b.dataset.ax) || Number(a.dataset.ay) - Number(b.dataset.ay));
      if (!bands.length || !["ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown", "Home", "End", "Escape"].includes(event.key)) return;
      event.preventDefault();
      if (event.key === "Escape") return hide(wrap);
      const current = bands.findIndex((n) => n.classList.contains("on"));
      const forward = event.key === "ArrowRight" || event.key === "ArrowDown";
      let next = event.key === "Home" ? 0 : event.key === "End" ? bands.length - 1 : current < 0 ? bands.length - 1 : current + (forward ? 1 : -1);
      next = Math.min(Math.max(next, 0), bands.length - 1);
      show(wrap, bands[next]);
    });
  }

  // On the child settings page, move focus to an import result so it is
  // read out and in view. The baby log does this for its own messages.
  const message = document.querySelector("[data-focus-on-load]");
  if (message && !message.closest("[data-baby-log]")) {
    message.focus({ preventScroll: true });
    message.scrollIntoView({ block: "center" });
  }

  const wraps = [...document.querySelectorAll("[data-chart]")];
  wraps.forEach((wrap) => { render(wrap); wire(wrap); });
  // A tap outside a chart puts its tooltip away on touch screens.
  document.addEventListener("pointerdown", (event) => {
    wraps.forEach((wrap) => { if (!wrap.contains(event.target)) hide(wrap); });
  });
  let timer = 0;
  window.addEventListener("resize", () => {
    clearTimeout(timer);
    timer = setTimeout(() => {
      wraps.forEach((wrap) => { if (draw[wrap.dataset.chart] && String(Math.max(260, Math.round(wrap.clientWidth))) !== wrap.dataset.drawn) render(wrap); });
    }, 150);
  });
})();
