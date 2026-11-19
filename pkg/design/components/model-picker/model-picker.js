// Model picker behavior: type to filter (matches in bold, sections kept, first match active), arrow keys, Home/End,
// Enter picks (the row blinks, the check moves, the trigger shows it), Esc clears the query, then closes. The pointer moves
// the one highlight. Anchored pickers sit under their trigger, right edges aligned, and open scrolled to the current model.
(() => {
  const esc = (s) => s.replace(/[&<>]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;" })[c]);
  const rows = (p) => [...p.querySelectorAll(".pk-row")].filter((r) => !r.hidden && !r.closest("[hidden]") && r.getAttribute("aria-disabled") !== "true");
  const setActive = (p, r, scroll) => {
    p.querySelectorAll(".pk-row.is-active").forEach((x) => x.classList.remove("is-active"));
    const input = p.querySelector("input");
    if (!r) { input?.removeAttribute("aria-activedescendant"); return; }
    r.classList.add("is-active");
    if (r.id) input?.setAttribute("aria-activedescendant", r.id);
    if (scroll) r.scrollIntoView({ block: "nearest" });
  };
  const filter = (p, q) => {
    q = q.trim().toLowerCase();
    for (const g of p.querySelectorAll(".pk-list > [role=group]")) {
      const sec = g.querySelector(".pk-label").textContent.toLowerCase();
      let any = false;
      for (const r of g.querySelectorAll(".pk-row")) {
        const n = r.querySelector(".pk-name"), label = (n.dataset.label ??= n.textContent);
        const hit = !q || `${label} ${r.querySelector(".pk-hint")?.textContent || ""} ${sec}`.toLowerCase().includes(q);
        r.hidden = !hit; any ||= hit;
        const i = q ? label.toLowerCase().indexOf(q) : -1;
        n.innerHTML = i < 0 ? esc(label) : esc(label.slice(0, i)) + "<b>" + esc(label.slice(i, i + q.length)) + "</b>" + esc(label.slice(i + q.length));
      }
      g.hidden = !any;
    }
    const list = p.querySelector(".pk-list");
    let none = p.querySelector(".pk-empty.pk-none");
    const empty = list && ![...list.children].some((g) => !g.hidden);
    if (empty && !none) { none = document.createElement("div"); none.className = "pk-empty pk-none"; list.after(none); }
    if (none) { none.hidden = !empty; none.textContent = `No models match “${q}”`; }
    if (list) list.hidden = empty;
    setActive(p, q ? rows(p).find((r) => r.closest(".pk-list")) : null, true);
  };
  const pick = (p, r) => {
    if (r.classList.contains("pk-more")) return;
    r.classList.add("is-blink");
    setTimeout(() => {
      r.classList.remove("is-blink");
      p.querySelectorAll('.pk-list [aria-selected="true"]').forEach((x) => x.setAttribute("aria-selected", "false"));
      r.setAttribute("aria-selected", "true");
      const t = p.id && document.querySelector(`[data-picker="${p.id}"]`);
      if (t) { t.firstChild.textContent = r.querySelector(".pk-name").dataset.label || r.querySelector(".pk-name").textContent; close(p); }
    }, 70);
  };
  const close = (p) => { const t = document.querySelector(`[data-picker="${p.id}"]`); if (!t) return; p.hidden = true; t.setAttribute("aria-expanded", "false"); t.classList.remove("is-open"); t.focus(); };
  const place = (p, t) => {
    const s = p.offsetParent.getBoundingClientRect(), b = t.getBoundingClientRect();
    p.style.top = b.bottom - s.top + 4 + "px";
    p.style.left = Math.max(8, b.right - s.left - p.offsetWidth + 2) + "px";
  };
  const open = (p, t) => {
    p.hidden = false; t.setAttribute("aria-expanded", "true"); t.classList.add("is-open");
    const input = p.querySelector("input"); input.value = ""; filter(p, "");
    place(p, t);
    const cur = p.querySelector('.pk-list [aria-selected="true"]');
    if (cur) { const l = p.querySelector(".pk-list"); l.scrollTop = cur.offsetTop - l.clientHeight / 2 + cur.offsetHeight / 2; setActive(p, cur); }
    input.focus({ preventScroll: true });
  };
  const init = (p) => {
    const input = p.querySelector("input");
    input?.addEventListener("input", () => filter(p, input.value));
    input?.addEventListener("keydown", (e) => {
      const rs = rows(p), i = rs.findIndex((r) => r.classList.contains("is-active"));
      const go = (j) => { e.preventDefault(); setActive(p, rs[Math.max(0, Math.min(rs.length - 1, j))], true); };
      if (e.key === "ArrowDown") go(i < 0 ? 0 : i + 1);
      else if (e.key === "ArrowUp") go(i < 0 ? rs.length - 1 : i - 1);
      else if (e.key === "Home" && !input.value) go(0);
      else if (e.key === "End" && !input.value) go(rs.length - 1);
      else if (e.key === "PageDown") go(i + 10);
      else if (e.key === "PageUp") go(i - 10);
      else if (e.key === "Enter" && i >= 0) { e.preventDefault(); pick(p, rs[i]); }
      else if (e.key === "Escape") { e.preventDefault(); if (input.value) { input.value = ""; filter(p, ""); } else close(p); }
    });
    p.addEventListener("pointermove", (e) => { const r = e.target.closest(".pk-row"); if (r && rows(p).includes(r) && !r.classList.contains("is-active")) setActive(p, r); });
    p.addEventListener("click", (e) => { const r = e.target.closest(".pk-row"); if (r && rows(p).includes(r)) pick(p, r); });
    // Static fixture states: center the current model in anchored pickers, line the effort submenu up with its row.
    const t = p.id && document.querySelector(`[data-picker="${p.id}"]`);
    if (t) {
      t.addEventListener("click", () => (p.hidden ? open(p, t) : close(p)));
      requestAnimationFrame(() => { if (p.hidden) return; place(p, t); const cur = p.querySelector('.pk-list [aria-selected="true"]'), l = p.querySelector(".pk-list"); if (cur) l.scrollTop = cur.offsetTop - l.clientHeight / 2 + cur.offsetHeight / 2; });
    }
    const pair = p.parentElement?.classList.contains("pk-pair") && p.nextElementSibling;
    if (pair) requestAnimationFrame(() => { const r = p.querySelector(".pk-more"); pair.style.marginTop = r.getBoundingClientRect().top - p.getBoundingClientRect().top - 6 + "px"; });
  };
  const run = () => document.querySelectorAll(".model-picker:not(.pk-sub)").forEach(init);
  document.readyState === "loading" ? document.addEventListener("DOMContentLoaded", run) : run();
})();
