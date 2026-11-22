#!/usr/bin/env python3
"""Convert the embedded application icon into a multi-size Windows .ico.

The installer and its shortcuts need an .ico; the application itself embeds the
PNG. Pillow is used when present because it resizes with high quality. Without
Pillow the script exits 3, and the release build simply ships no installer
icon (a cosmetic loss, not a broken package).
"""
import sys
from pathlib import Path

SIZES = (16, 24, 32, 48, 64, 128, 256)


def main() -> int:
    if len(sys.argv) != 3:
        print("usage: make-ico.py <appicon.png> <out.ico>", file=sys.stderr)
        return 2
    src, dst = Path(sys.argv[1]), Path(sys.argv[2])
    try:
        from PIL import Image
    except ImportError:
        print("make-ico.py: Pillow is not installed; skipping the installer icon", file=sys.stderr)
        return 3

    image = Image.open(src).convert("RGBA")
    # Downscale from the largest source so every frame is crisp; the 256 px
    # frame is stored as PNG, the smaller ones as DIB (the shell accepts both).
    frames = [image.resize((s, s), Image.LANCZOS) for s in SIZES]
    frames[-1].save(dst, format="ICO", sizes=[(s, s) for s in SIZES], append_images=frames[:-1])
    print(f"make-ico.py: wrote {dst} ({', '.join(f'{s}x{s}' for s in SIZES)})")
    return 0


if __name__ == "__main__":
    sys.exit(main())