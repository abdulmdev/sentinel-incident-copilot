"""Assembles docs/media/closed-loop.gif from the frames recorded by record-demo.mjs.

  cd docs/build && node record-demo.mjs && uv run --no-project --with pillow python make-gif.py

Each frame gets a caption bar with the step number; ffmpeg's two-pass palette keeps text sharp and
the file small.
"""
import json
import pathlib
import subprocess

from PIL import Image, ImageDraw, ImageFont

HERE = pathlib.Path(__file__).parent
FRAMES = HERE / "frames"
OUT = HERE.parent / "media" / "closed-loop.gif"
WIDTH = 1080
BAR = 64
ACCENT = (47, 91, 211)
INK = (24, 24, 27)


def font(size: int, bold: bool = False):
    for path in (["/System/Library/Fonts/SFNS.ttf", "/System/Library/Fonts/Helvetica.ttc"] if not bold else
                 ["/System/Library/Fonts/SFNSRounded.ttf", "/System/Library/Fonts/Helvetica.ttc"]):
        try:
            return ImageFont.truetype(path, size)
        except OSError:
            continue
    return ImageFont.load_default()


def caption(src: pathlib.Path, text: str, step: str, dst: pathlib.Path) -> None:
    img = Image.open(src).convert("RGB")
    img = img.resize((WIDTH, round(img.height * WIDTH / img.width)), Image.LANCZOS)
    canvas = Image.new("RGB", (WIDTH, img.height + BAR), INK)
    canvas.paste(img, (0, 0))
    d = ImageDraw.Draw(canvas)
    y = img.height
    if step:
        d.ellipse((18, y + 14, 54, y + 50), fill=ACCENT)
        d.text((36, y + 32), step, font=font(20, True), fill="white", anchor="mm")
    d.text((70 if step else 20, y + 32), text, font=font(22), fill="white", anchor="lm")
    canvas.save(dst)


def main() -> None:
    meta = json.loads((FRAMES / "frames.json").read_text())
    captioned = FRAMES / "captioned"
    captioned.mkdir(exist_ok=True)
    lines = []
    for f in meta["frames"]:
        dst = captioned / f["file"]
        caption(FRAMES / f["file"], f["caption"], f["step"], dst)
        lines += [f"file '{dst}'", f"duration {f['seconds']}"]
    lines.append(f"file '{captioned / meta['frames'][-1]['file']}'")  # concat demuxer needs the last frame twice
    listing = FRAMES / "list.txt"
    listing.write_text("\n".join(lines) + "\n")
    OUT.parent.mkdir(parents=True, exist_ok=True)
    palette = FRAMES / "palette.png"
    subprocess.run(["ffmpeg", "-y", "-loglevel", "error", "-f", "concat", "-safe", "0", "-i", str(listing),
                    "-vf", "palettegen=max_colors=128:stats_mode=full", str(palette)], check=True)
    subprocess.run(["ffmpeg", "-y", "-loglevel", "error", "-f", "concat", "-safe", "0", "-i", str(listing),
                    "-i", str(palette), "-lavfi", "paletteuse=dither=none", "-loop", "0", str(OUT)], check=True)
    print(f"{OUT} ({OUT.stat().st_size / 1e6:.1f} MB, {len(meta['frames'])} frames, mitigated in {meta['ttm']})")


if __name__ == "__main__":
    main()
