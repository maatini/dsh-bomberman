# Vendored browser assets

These files are checked in so Bomber Grid works fully offline and without a Node
toolchain.

| File | Version | Upstream |
| --- | --- | --- |
| `htmx.min.js` | 2.0.4 | https://github.com/bigskysoftware/htmx |
| `htmx-ws.min.js` | 2.0.3 | https://github.com/bigskysoftware/htmx-extensions (`ws` extension) |
| `idiomorph-ext.min.js` | 0.3.0 | https://github.com/bigskysoftware/idiomorph |

htmx and Idiomorph are distributed under the BSD 2-Clause license; the license
texts are included next to this file. The `ws` extension is part of the
bigskysoftware htmx-extensions project and is distributed under the same
permissive terms as htmx.

The HTMX WebSocket extension is used for all live updates and form messages.
Idiomorph is registered as the `morph` swap extension so the 15 Hz HTML frames
update stable DOM nodes instead of replacing the board and losing CSS
transitions.
