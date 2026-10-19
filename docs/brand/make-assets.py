import sys
from PIL import Image
src, out = sys.argv[1], sys.argv[2]
im = Image.open(src).convert("RGB")
W, H = im.size
px = im.load()
# The source has a stray grey line along its top edge.
for y in range(3):
    for x in range(W):
        px[x, y] = (0, 0, 0)

T = 100  # at or above this brightness a pixel is solid; below it, it fades out


def cutout(img):
    """The image over black, as the same colours over transparency: solid
    parts keep their colours; dim edges and glows become partly transparent
    (colour brightened back so they don't go grey)."""
    p = img.load()
    t = Image.new("RGBA", img.size)
    tp = t.load()
    for y in range(img.size[1]):
        for x in range(img.size[0]):
            r, g, b = p[x, y]
            m = max(r, g, b)
            if m <= 6:
                tp[x, y] = (0, 0, 0, 0)
            elif m >= T:
                tp[x, y] = (r, g, b, 255)
            else:
                a = m / T
                tp[x, y] = (min(255, round(r / a)), min(255, round(g / a)), min(255, round(b / a)), round(a * 255))
    return t


t = cutout(im)
bbox = t.getchannel("A").point(lambda v: 255 if v > 24 else 0).getbbox()
pad = 10
box = (max(0, bbox[0] - pad), max(0, bbox[1] - pad), min(W, bbox[2] + pad), min(H, bbox[3] + pad))
t.crop(box).save(f"{out}/logo.png", optimize=True)        # transparent: use this
im.crop(box).save(f"{out}/logo-black.png", optimize=True)  # on black
im.save(f"{out}/logo-square-black.png", optimize=True)     # the original, square

def fade(t, frac):
    """Fade the left and right edges out, so the conduits run on past it."""
    w, h = t.size
    p = t.load()
    f = max(1, int(w * frac))
    for y in range(h):
        for x in range(w):
            d = min(x, w - 1 - x)
            if d < f:
                r, g, b, a = p[x, y]
                p[x, y] = (r, g, b, round(a * (d / f) ** 1.3))
    return t


# The mark: the sprout, the hub and the conduits leaving it sideways (the
# bus lines and the inner nodes), fading out at the sides.
m = Image.new("RGB", im.size)
mp = m.load()
for y in range(365, 557):
    for x in range(352, 673):
        mp[x, y] = px[x, y]
mt = cutout(m)
bb = mt.getchannel("A").point(lambda v: 255 if v > 24 else 0).getbbox()
wide = fade(mt.crop((bb[0] - 2, bb[1] - 2, bb[2] + 2, bb[3] + 2)), 0.16)
wide.resize((wide.width * 160 // wide.height, 160), Image.LANCZOS).save(f"{out}/mark-wide.png", optimize=True)
cx, cy = (bb[0] + bb[2]) // 2, (bb[1] + bb[3]) // 2
half = max(bb[2] - bb[0], bb[3] - bb[1]) // 2 + 4
mark = fade(mt.crop((cx - half, cy - half, cx + half, cy + half)), 0.16)
mark.resize((256, 256), Image.LANCZOS).save(f"{out}/mark.png", optimize=True)
for s in (16, 32, 48):
    mark.resize((s, s), Image.LANCZOS).save(f"{out}/favicon-{s}.png", optimize=True)
mark.resize((256, 256), Image.LANCZOS).save(f"{out}/favicon.ico", sizes=[(16, 16), (32, 32), (48, 48)])
# iOS home screen icons don't do transparency: the mark on black, with room.
bg = Image.new("RGBA", (180, 180), (0, 0, 0, 255))
bg.alpha_composite(mark.resize((140, 140), Image.LANCZOS), (20, 20))
bg.convert("RGB").save(f"{out}/apple-touch-icon.png", optimize=True)
print("logo", box, "mark", bb)
