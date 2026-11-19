// Copy puts a component's markup on the clipboard.
document.addEventListener("click", async (e) => {
  const b = e.target.closest("button.copy");
  if (!b) return;
  await navigator.clipboard.writeText(await (await fetch(b.dataset.src)).text());
  b.textContent = "Copied";
  setTimeout(() => (b.textContent = "Copy"), 1500);
});
// The needs card demo prints what a dashboard would send.
document.addEventListener("mp-reply", (e) => {
  const w = e.target.closest(".pn")?.querySelector(".nc-wire");
  if (w) w.textContent = "send(text):\n" + e.detail.text;
});
