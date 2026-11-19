// Stepper: .stepper[role=spinbutton] holds the value in aria-valuenow (clamped to aria-valuemin/max) and shows it in the
// <output> of its .stepper-field. Click steps once; press and hold repeats (after 400 ms, every 80 ms, as NSStepper and
// UIStepper autorepeat); keys: arrows step, Home/End jump to min/max. The end button is disabled at min/max.
// Fires "change" on the .stepper with detail = the new value.
(() => {
  const num = (st, a, d) => (st.hasAttribute(a) ? +st.getAttribute(a) : d);
  const sync = (st) => {
    const v = num(st, "aria-valuenow", 0), off = st.getAttribute("aria-disabled") === "true";
    st.querySelectorAll("[data-step]").forEach((b) => (b.disabled = off || (+b.dataset.step < 0 ? v <= num(st, "aria-valuemin", -Infinity) : v >= num(st, "aria-valuemax", Infinity))));
    const o = st.closest(".stepper-field")?.querySelector("output"); if (o) o.value = v;
  };
  const set = (st, v) => {
    v = Math.min(num(st, "aria-valuemax", Infinity), Math.max(num(st, "aria-valuemin", -Infinity), v));
    if (v === num(st, "aria-valuenow", 0) || st.getAttribute("aria-disabled") === "true") return false;
    st.setAttribute("aria-valuenow", v); sync(st); st.dispatchEvent(new CustomEvent("change", { bubbles: true, detail: v }));
    return true;
  };
  let timer;
  const stop = () => clearTimeout(timer);
  document.addEventListener("pointerdown", (e) => {
    const b = e.target.closest?.(".stepper [data-step]"); if (!b || b.disabled || e.button) return;
    e.preventDefault(); // no focus ring and no text selection on mouse; keyboard focus stays on the spinbutton
    const st = b.closest(".stepper"), d = +b.dataset.step;
    const tick = (ms) => { if (set(st, num(st, "aria-valuenow", 0) + d)) timer = setTimeout(() => tick(80), ms); };
    tick(400);
  });
  ["pointerup", "pointercancel", "dragstart", "blur"].forEach((t) => addEventListener(t, stop));
  document.addEventListener("pointerleave", (e) => e.target.matches?.(".stepper [data-step]") && stop(), true);
  document.addEventListener("keydown", (e) => {
    const st = e.target.closest?.(".stepper[role=spinbutton]"); if (!st) return;
    const v = num(st, "aria-valuenow", 0);
    const n = { ArrowUp: v + 1, ArrowRight: v + 1, ArrowDown: v - 1, ArrowLeft: v - 1, Home: num(st, "aria-valuemin", v), End: num(st, "aria-valuemax", v) }[e.key];
    if (n === undefined) return;
    e.preventDefault(); set(st, n);
  });
  const all = () => document.querySelectorAll(".stepper").forEach(sync);
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", all); else all();
  window.MPStepper = { sync: all, set };
})();
