# Alert
Rare. Two choices side by side.

Matched 260 pt card, 18 pt corners, bold title, capsule buttons filling the width, red text on a pink capsule.

## CSS notes
Alert: centered card over a scrim, about 280 pt wide, radius 18, bold title, body, buttons side by side filling the width (macOS 26).
<div class="scrim"><div class="alert" role="alertdialog" aria-labelledby=..><mp-icon/><h2>..</h2><p>..</p><label class="check-row">..</label><div class="alert-actions"><button class="btn lg">Cancel</button><button class="btn lg tinted danger">Delete</button></div></div></div>
Needs button.css. Three or more actions stack.
