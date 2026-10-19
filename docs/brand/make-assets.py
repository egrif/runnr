"""Makes runnr's brand assets from the original artwork, as plain crops and
resizes of it (no recolouring). Usage: python make-assets.py original.png out-dir
(needs Pillow)."""
import sys
from PIL import Image

src, out = sys.argv[1], sys.argv[2]
im = Image.open(src).convert("RGB")
W, H = im.size
im.save(f"{out}/logo-square.png", optimize=True)  # the original

# The logo: the whole graphic, cropped to it (with a margin of black).
logo = im.crop((32, 356, 992, 671))
logo.save(f"{out}/logo.png", optimize=True)

# The mark: the sprout and hub with the conduits leaving it.
wide = im.crop((340, 352, 684, 572))
wide.resize((wide.width * 160 // wide.height, 160), Image.LANCZOS).save(f"{out}/mark-wide.png", optimize=True)
square = im.crop((392, 350, 632, 590))
square.resize((256, 256), Image.LANCZOS).save(f"{out}/mark.png", optimize=True)
for s in (16, 32, 48):
    square.resize((s, s), Image.LANCZOS).save(f"{out}/favicon-{s}.png", optimize=True)
square.resize((256, 256), Image.LANCZOS).save(f"{out}/favicon.ico", sizes=[(16, 16), (32, 32), (48, 48)])
square.resize((180, 180), Image.LANCZOS).save(f"{out}/apple-touch-icon.png", optimize=True)
print("ok")
