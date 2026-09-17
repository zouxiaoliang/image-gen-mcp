"""Render the recorded local MCP demo as a README GIF. Requires Pillow."""

import json
import os
from pathlib import Path
from PIL import Image, ImageDraw, ImageFont


ROOT = Path(__file__).resolve().parents[1]
ASSETS = ROOT / "docs" / "assets"


def font(size):
    candidates = [
        os.environ.get("DEMO_FONT", ""),
        "/System/Library/Fonts/Supplemental/Arial.ttf",
        "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
        "C:/Windows/Fonts/arial.ttf",
    ]
    for candidate in candidates:
        if candidate and Path(candidate).is_file():
            return ImageFont.truetype(candidate, size)
    return ImageFont.load_default(size=size)


def render(events, active):
    canvas = Image.new("RGB", (1200, 700), "#0c1424")
    draw = ImageDraw.Draw(canvas)
    draw.text((44, 30), "image-gen-mcp", font=font(38), fill="#f3f6ff")
    draw.text((44, 82), "Generate. Edit. Keep the task.", font=font(21), fill="#a6b8d2")
    draw.rounded_rectangle((920, 37, 1156, 75), radius=18, fill="#1b3c3b")
    draw.text((943, 44), "MCP / Go / SQLite", font=font(18), fill="#91e4c5")
    draw.line((44, 127, 1156, 127), fill="#263650", width=1)
    draw.text((44, 147), "LOCAL REPLAY", font=font(15), fill="#91e4c5")
    draw.text((210, 147), "Real MCP subprocess. Saved provider images. No API key.", font=font(17), fill="#a6b8d2")

    for index, event in enumerate(events):
        y = 207 + index * 76
        enabled = index <= active
        draw.rounded_rectangle((44, y, 76, y + 32), radius=8, fill="#24483f" if enabled else "#1c283c")
        draw.text((53, y + 5), str(index + 1), font=font(18), fill="#91e4c5" if enabled else "#657791")
        draw.text((91, y), event["title"], font=font(21), fill="#f3f6ff" if enabled else "#657791")
        # Recorded details remain available in JSON; short captions fit on GitHub.
        captions = [
            "Connected to the server over stdio",
            "Five tools and a configured model",
            "Completed / preview and original verified",
            "Reference supplied / edited result verified",
            "Same task / no additional upstream request",
        ]
        if enabled:
            draw.text((91, y + 31), captions[index], font=font(16), fill="#a6b8d2")

    draw.rounded_rectangle((704, 195, 1156, 596), radius=18, fill="#172338")
    current = events[active].get("image")
    if current:
        with Image.open(ASSETS / current) as source:
            preview = source.convert("RGB").resize((324, 324), Image.Resampling.LANCZOS)
        canvas.paste(preview, (768, 214))
        label = "GENERATED / RED SQUARE" if current == "generated.png" else "EDITED / BLUE CIRCLE"
        draw.text((751, 559), label, font=font(17), fill="#91e4c5")
    else:
        draw.text((779, 354), "Ready for an image", font=font(24), fill="#a6b8d2")
        draw.text((807, 393), "Discover / call / poll", font=font(18), fill="#657791")

    draw.text((44, 640), "stdio + Streamable HTTP", font=font(18), fill="#a6b8d2")
    draw.text((704, 640), "Local replay / timing adjusted for readability", font=font(16), fill="#a6b8d2")
    return canvas


def main():
    events = json.loads((ASSETS / "demo-recording.json").read_text())
    if len(events) != 5 or events[2].get("image") != "generated.png" or events[3].get("image") != "edited.png":
        raise ValueError("Run examples/demo -record docs/assets/demo-recording.json first")
    frames = [render(events, step) for step in range(len(events))]
    frames[0].save(
        ASSETS / "demo.gif", save_all=True, append_images=frames[1:],
        duration=[1700, 1700, 2700, 2700, 3800], loop=0, optimize=True,
    )
    frames[-1].save(ASSETS / "demo-poster.png")
    print(ASSETS / "demo.gif")


if __name__ == "__main__":
    main()
