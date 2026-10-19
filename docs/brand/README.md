# runnr brand assets

The logo is a sprout growing from a circuit: runbooks that grow and run.
Every file here is a crop (and resize) of the original artwork. Its black
background is made transparent; nothing else is redrawn or recoloured
(glows and anti-aliased edges, which were partly black, become partly
transparent, so they blend into light and dark backgrounds alike).

| File | Use |
| --- | --- |
| `logo.png` | The whole graphic, transparent. Use this almost everywhere: banners, the home screen. |
| `mark.png` | The sprout and hub with the conduits leaving it, transparent, square (256×256): favicons. |
| `mark-wide.png` | The same, cropped wide, for beside the wordmark. |
| `logo-black.png` | `logo.png` on its original black, where a solid background is needed. |
| `logo-square.png` | The original artwork (1024×1024, on black), e.g. for avatars and social cards. |

The web app's copies (favicon sizes; the iOS icon, on black since iOS
icons can't be transparent) are in `internal/server/web/brand/`; the HTML
view embeds a small `mark-wide`. `make-assets.py` makes them all from the
original: `python make-assets.py original.png out-dir` (it needs Pillow).

The brand green is `#277a3e` on light backgrounds (white text on it) and
`#5cc26f` on dark ones.
