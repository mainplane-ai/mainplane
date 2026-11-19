// HTML has no indeterminate attribute: <input class="checkbox" data-indeterminate> gets input.indeterminate = true.
for (const i of document.querySelectorAll("input.checkbox[data-indeterminate]")) i.indeterminate = true;
