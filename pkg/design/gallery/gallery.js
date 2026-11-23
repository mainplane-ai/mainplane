// Copy puts a component's markup on the clipboard.
document.addEventListener("click", async (e) => {
  const b = e.target.closest("button.copy");
  if (!b) return;
  await navigator.clipboard.writeText(await (await fetch(b.dataset.src)).text());
  b.textContent = "Copied";
  setTimeout(() => (b.textContent = "Copy"), 1500);
});
