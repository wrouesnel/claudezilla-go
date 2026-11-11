#!/usr/bin/env python3
"""Build a reproducible, unsigned XPI from extension/.

Same tree in, byte-identical XPI out: files are sorted, timestamps and
permissions fixed, and nothing outside extension/ is included. Prints the
output path and its sha256 (the value the update manifest needs once the
signed copy comes back from Mozilla).

    build-xpi.py [--update-url URL]

--update-url (or $CLAUDEZILLA_UPDATE_URL) writes gecko.update_url into the
packaged manifest, for self-hosted updates. Without it the XPI has none and
Firefox never updates it.
"""
import argparse, hashlib, json, os, pathlib, sys, zipfile

ap = argparse.ArgumentParser()
ap.add_argument("--update-url", default=os.environ.get("CLAUDEZILLA_UPDATE_URL"))
args = ap.parse_args()
if args.update_url and not args.update_url.startswith("https://"):
    sys.exit("--update-url must be https://")

root = pathlib.Path(__file__).resolve().parent.parent
src = root / "extension"
out_dir = root / "dist"
manifest = json.loads((src / "manifest.json").read_text())
ext_id = manifest["browser_specific_settings"]["gecko"]["id"]
version = manifest["version"]
out = out_dir / f"{ext_id}-{version}.unsigned.xpi"

SKIP = {".DS_Store", "CLAUDE.md"}
files = sorted(p for p in src.rglob("*") if p.is_file() and p.name not in SKIP and not p.name.endswith((".swp", ".swo")))
out_dir.mkdir(exist_ok=True)
with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED, compresslevel=9) as z:
    for p in files:
        info = zipfile.ZipInfo(p.relative_to(src).as_posix(), date_time=(1980, 1, 1, 0, 0, 0))
        info.external_attr = 0o644 << 16
        info.compress_type = zipfile.ZIP_DEFLATED
        data = p.read_bytes()
        if p == src / "manifest.json" and args.update_url:
            m = json.loads(data)
            m["browser_specific_settings"]["gecko"]["update_url"] = args.update_url
            data = (json.dumps(m, indent=2) + "\n").encode()
        z.writestr(info, data)
digest = hashlib.sha256(out.read_bytes()).hexdigest()
print(f"{out}\nsha256:{digest}\n{len(files)} files")
