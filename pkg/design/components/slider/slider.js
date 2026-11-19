// Keeps --v (0..1) on every input.slider so the track fills to the knob centre. Works for sliders added later (event delegation).
(() => {
  const set = (s) => s.style.setProperty("--v", (s.value - (s.min || 0)) / ((s.max || 100) - (s.min || 0)));
  const all = () => document.querySelectorAll("input.slider").forEach(set);
  document.addEventListener("input", (e) => e.target.matches?.("input.slider") && set(e.target));
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", all); else all();
  window.MPSlider = { sync: all };
})();
