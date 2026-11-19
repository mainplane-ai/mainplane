// Composer pill: .tall when it is taller than one line (31), so CSS can switch the capsule ends to a continuous corner.
(() => {
  const ro = new ResizeObserver((es) => es.forEach((e) => e.target.classList.toggle("tall", e.target.offsetHeight > 32)));
  const scan = () => document.querySelectorAll(".composer .field").forEach((f) => ro.observe(f));
  document.readyState === "loading" ? document.addEventListener("DOMContentLoaded", scan) : scan();
  document.addEventListener("focusin", (e) => { const f = e.target.closest?.(".composer .field"); if (f) ro.observe(f); }); // composers made later
})();
