// Needs you card: Reply asks the interface to put the question's title over the chat box under the
// dashboard (mainplane.call("reply")), and the user answers there. Outside a dashboard it does nothing.
document.addEventListener("click", (e) => {
  const b = e.target.closest(".need .btn.reply");
  if (b) window.mainplane?.call("reply", { title: b.closest(".need").querySelector("h3").textContent });
});
