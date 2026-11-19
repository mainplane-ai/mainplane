# Popover
Floating panel with an arrow toward the source. Two looks to pick from: frosted (the one frosted material) and solid.

Matched macOS 26 popover: 16 pt corners, arrow, padding 12, soft shadow. Frosted = backdrop blur + paper/ink fill (no refraction); solid = the Reduce Transparency fill.

## CSS notes
Popover: floating panel with an arrow toward its source (.down flips it), radius 16, a shadow because it floats.
<div class="popover" style="--arrow-x:30px"><div class="body">..</div></div>
Solid floating fill by default; .frosted = the one frosted material (backdrop blur + paper/ink fill; solid under Reduce Transparency).
The arrow is the wrapper's ::before with its own backdrop: a child of a backdrop-filter element cannot see the page under it.
