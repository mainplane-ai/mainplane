// Overlay scrollbars for .scroller (not .always), like macOS 26: data-sb="on" while scrolling, hidden 0.8 s after
// the last scroll (the CSS fades it); data-sb="bar" (thumb darkens, stays) while the pointer is on a shown bar or drags it.
(() => {
  const HOLD = 800, BAR = 17, timers = new WeakMap();
  let over = null, drag = null;
  const set = (el, v) => (v ? (el.dataset.sb = v) : delete el.dataset.sb);
  const later = (el) => { clearTimeout(timers.get(el)); timers.set(el, setTimeout(() => el !== over && el !== drag && set(el, ""), HOLD)); };
  const leave = (el) => { if (el && el !== over && el !== drag) { set(el, "on"); later(el); } };
  const onBar = (el, e) => {
    const root = el === document.documentElement, r = root ? new DOMRect(0, 0, innerWidth, innerHeight) : el.getBoundingClientRect();
    const s = getComputedStyle(el), right = r.right - (root ? 0 : parseFloat(s.borderRightWidth)), bottom = r.bottom - (root ? 0 : parseFloat(s.borderBottomWidth));
    return (el.scrollHeight > el.clientHeight && e.clientX >= right - BAR && e.clientX < right) || (el.scrollWidth > el.clientWidth && e.clientY >= bottom - BAR && e.clientY < bottom);
  };
  addEventListener("scroll", (e) => {
    const el = e.target === document ? document.documentElement : e.target;
    if (!el.classList?.contains("scroller") || el.classList.contains("always")) return;
    if (el.dataset.sb !== "bar") set(el, "on");
    later(el);
  }, { capture: true, passive: true });
  addEventListener("pointermove", (e) => {
    if (drag && !e.buttons) { const d = drag; drag = null; leave(d); }
    if (drag) return;
    // like Messages: the pointer keeps and darkens a shown bar, but never reveals a hidden one
    const el = e.target.closest?.(".scroller:not(.always, .sb-static)"), hit = el && (el === over || el.dataset.sb) && onBar(el, e) ? el : null;
    if (hit === over) return;
    const old = over; over = hit; leave(old);
    if (hit) { clearTimeout(timers.get(hit)); set(hit, "bar"); }
  }, { passive: true });
  addEventListener("pointerdown", () => { drag = over; }, true);
  addEventListener("pointerup", () => { const d = drag; drag = null; leave(d); }, true);
  document.documentElement.addEventListener("pointerleave", () => { const o = over; over = null; leave(o); });
})();
