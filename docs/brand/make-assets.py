"""Makes runnr's brand assets from the original artwork: crops (and
resizes) of it, with the black background made transparent and nothing
else changed. Usage: python make-assets.py original.png out-dir (needs
Pillow)."""
import sys
from PIL import Image

src, out = sys.argv[1], sys.argv[2]
im = Image.open(src).convert("RGB")
W, H = im.size
im.save(f"{out}/logo-square.png", optimize=True)  # the original, on black

# Pixels at least this bright are solid, in their own colours: greys from
# T_GREY, greens from T_GREEN (so a dim green glow becomes a light green
# tint, not a grey smudge, over light backgrounds).
T_GREY, T_GREEN = 100, 170


def transparent(img):
    """The background (black) becomes transparent. Solid parts keep their
    colours exactly; anti-aliased edges and glows, which are partly black,
    become partly transparent instead, so they blend into any background."""
    p = img.load()
    t = Image.new("RGBA", img.size)
    tp = t.load()
    for y in range(img.size[1]):
        for x in range(img.size[0]):
            r, g, b = p[x, y]
            m = max(r, g, b)
            T = T_GREEN if m and (m - min(r, g, b)) / m > 0.35 else T_GREY
            if m <= 6:
                tp[x, y] = (0, 0, 0, 0)
            elif m >= T:
                tp[x, y] = (r, g, b, 255)
            else:
                a = m / T
                tp[x, y] = (min(255, round(r / a)), min(255, round(g / a)), min(255, round(b / a)), round(a * 255))
    return t


# The logo: the whole graphic.
logo = im.crop((32, 356, 992, 671))
logo.save(f"{out}/logo-black.png", optimize=True)
transparent(logo).save(f"{out}/logo.png", optimize=True)

# The mark: the sprout and hub with the conduits leaving it.
wide = transparent(im.crop((340, 352, 684, 572)))
wide.resize((wide.width * 160 // wide.height, 160), Image.LANCZOS).save(f"{out}/mark-wide.png", optimize=True)
square = im.crop((392, 350, 632, 590))
sq = transparent(square)
sq.resize((256, 256), Image.LANCZOS).save(f"{out}/mark.png", optimize=True)
for s in (16, 32, 48):
    sq.resize((s, s), Image.LANCZOS).save(f"{out}/favicon-{s}.png", optimize=True)
sq.resize((256, 256), Image.LANCZOS).save(f"{out}/favicon.ico", sizes=[(16, 16), (32, 32), (48, 48)])
# iOS home screen icons can't be transparent: the crop on its black.
square.resize((180, 180), Image.LANCZOS).save(f"{out}/apple-touch-icon.png", optimize=True)
print("ok")
