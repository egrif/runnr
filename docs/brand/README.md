# runnr brand assets

The logo is a sprout growing from a circuit: runbooks that grow and run.

| File | Use |
| --- | --- |
| `logo.png` | The logo on a transparent background. Use this almost everywhere (it works on light and dark). |
| `logo-black.png` | The logo on black, cropped like `logo.png`, for places that need a solid background. |
| `logo-square-black.png` | The original square artwork on black (1024×1024), e.g. for avatars and social cards. |
| `mark.png` | The mark: the sprout and hub with the conduits leaving it, fading out at the sides; transparent, square (256×256). Favicons and app icons. |
| `mark-wide.png` | The same mark cropped wide (no empty space above and below), for beside the wordmark. |

The web app's copies (favicon sizes, the iOS icon on black) are in
`internal/server/web/brand/`; the HTML view embeds a 64px `mark.png`.
`make-assets.py` makes them all from the original artwork (it needs
Pillow): `python make-assets.py original.png out-dir`.

The brand green is `#277a3e` on light backgrounds (white text on it) and
`#5cc26f` on dark ones.
