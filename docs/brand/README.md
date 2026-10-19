# runnr brand assets

The logo is a sprout growing from a circuit: runbooks that grow and run.
Every file here is a crop (and resize) of the original artwork, on its
black background; nothing is redrawn or recoloured.

| File | Use |
| --- | --- |
| `logo-square.png` | The original artwork (1024×1024), e.g. for avatars and social cards. |
| `logo.png` | The whole graphic, cropped wide: banners, the home screen. |
| `mark.png` | The sprout and hub with the conduits leaving it, square (256×256): favicons and app icons. |
| `mark-wide.png` | The same, cropped wide, for beside the wordmark. |

On light backgrounds they sit as dark tiles with rounded corners. The web
app's copies (favicon sizes, the iOS icon) are in
`internal/server/web/brand/`; the HTML view embeds a small `mark-wide`.
`make-assets.py` makes them all from the original:
`python make-assets.py original.png out-dir` (it needs Pillow).

The brand green is `#277a3e` on light backgrounds (white text on it) and
`#5cc26f` on dark ones.
