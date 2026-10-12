#!/usr/bin/env python3
"""Rebuild the pinned Fluent sprite; development only, never run by the server."""

import hashlib
from pathlib import Path
import subprocess
import tarfile
import tempfile
import xml.etree.ElementTree as ET


VERSION = "1.1.344"
TARBALL = f"https://registry.npmjs.org/@fluentui/svg-icons/-/svg-icons-{VERSION}.tgz"
TARBALL_SHA = "6fe61f489a6d0d065ec2182794fdadf0e15db7d3388997c5ba8039dcfaaa35e8"
# npm omits LICENSE; gitHead in the pinned package metadata identifies its source.
LICENSE_URL = (
    "https://raw.githubusercontent.com/microsoft/fluentui-system-icons/"
    "f1260014e8a4fcfa6777182fd6cf082fc5e952d8/LICENSE"
)
LICENSE_SHA = "69bc45dc42b9acb96a69823adbc6ae538374e3c0bde169b855b32c48eaaef52f"
ICONS = (
    "home", "add", "search", "settings", "people", "panel_right", "info",
    "chevron_down", "chevron_right", "checkmark", "send", "arrow_up",
    "branch", "dismiss", "pin", "sign_out",
)


def fetch(url, path, expected_sha):
    subprocess.run(["curl", "-fsSL", url, "-o", str(path)], check=True)
    data = path.read_bytes()
    if hashlib.sha256(data).hexdigest() != expected_sha:
        raise ValueError(f"SHA-256 mismatch: {url}")
    return data


def main():
    vendor = Path(__file__).resolve().parents[1] / "web/static/vendor"
    namespace = "http://www.w3.org/2000/svg"
    ET.register_namespace("", namespace)
    sprite = ET.Element(f"{{{namespace}}}svg")
    with tempfile.TemporaryDirectory(prefix="ribbitto-icons-") as directory:
        tarball = Path(directory) / "icons.tgz"
        fetch(TARBALL, tarball, TARBALL_SHA)
        license_data = fetch(LICENSE_URL, Path(directory) / "LICENSE", LICENSE_SHA)
        with tarfile.open(tarball) as archive:
            for name in ICONS:
                for style in ("regular", "filled"):
                    with archive.extractfile(f"package/icons/{name}_20_{style}.svg") as source:
                        svg = ET.parse(source).getroot()
                    symbol = ET.SubElement(sprite, f"{{{namespace}}}symbol", {
                        "id": f"{name.replace('_', '-')}-{style}",
                        "viewBox": svg.attrib["viewBox"],
                        "fill": "currentColor",
                    })
                    symbol.extend(svg)
    ET.indent(sprite)
    (vendor / f"fluent-icons-{VERSION}.svg").write_bytes(
        ET.tostring(sprite, encoding="utf-8") + b"\n"
    )
    (vendor / "fluent-icons-LICENSE").write_bytes(license_data)


if __name__ == "__main__":
    main()
