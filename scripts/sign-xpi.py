#!/usr/bin/env python3
"""Have Mozilla sign an XPI as an unlisted add-on, so release Firefox installs it.

    AMO_JWT_ISSUER=... AMO_JWT_SECRET=... scripts/sign-xpi.py dist/<id>-<ver>.unsigned.xpi

Unlisted means Mozilla signs the file but doesn't list it on addons.mozilla.org.
Get the two values at https://addons.mozilla.org/developers/addon/api/key/.
They are read from the environment only, never from arguments.

Steps (AMO API v5): upload the XPI on the unlisted channel, wait for
validation, create the add-on or add a version (PUT addon/<guid>), wait for
signing, then download the signed file next to the input as
<id>-<ver>.signed.xpi and print its path and sha256. Standard library only.
"""
import base64, hashlib, hmac, json, os, pathlib, secrets, sys, time, urllib.error, urllib.request, uuid, zipfile

API = os.environ.get("AMO_API", "https://addons.mozilla.org/api/v5")
TIMEOUT_S = int(os.environ.get("AMO_TIMEOUT_S", "900"))


def b64url(b: bytes) -> str:
    return base64.urlsafe_b64encode(b).rstrip(b"=").decode()


def jwt() -> str:
    iss, secret = os.environ.get("AMO_JWT_ISSUER"), os.environ.get("AMO_JWT_SECRET")
    if not iss or not secret:
        sys.exit("Set AMO_JWT_ISSUER and AMO_JWT_SECRET (from addons.mozilla.org/developers/addon/api/key/)")
    now = int(time.time())
    head = b64url(json.dumps({"alg": "HS256", "typ": "JWT"}).encode())
    body = b64url(json.dumps({"iss": iss, "jti": secrets.token_hex(16), "iat": now, "exp": now + 60}).encode())
    sig = b64url(hmac.new(secret.encode(), f"{head}.{body}".encode(), hashlib.sha256).digest())
    return f"{head}.{body}.{sig}"


def request(method, url, data=None, ctype=None, raw=False):
    if not url.startswith("http"):
        url = API + url
    req = urllib.request.Request(url, data=data, method=method)
    req.add_header("Authorization", "JWT " + jwt())
    if ctype:
        req.add_header("Content-Type", ctype)
    try:
        with urllib.request.urlopen(req, timeout=120) as r:
            body = r.read()
    except urllib.error.HTTPError as e:
        sys.exit(f"{method} {url}: HTTP {e.code}: {e.read().decode(errors='replace')[:2000]}")
    return body if raw else json.loads(body or b"{}")


def multipart(fields, file_field, path):
    boundary = uuid.uuid4().hex
    parts = []
    for k, v in fields.items():
        parts.append(f'--{boundary}\r\nContent-Disposition: form-data; name="{k}"\r\n\r\n{v}\r\n'.encode())
    parts.append(
        f'--{boundary}\r\nContent-Disposition: form-data; name="{file_field}"; filename="{path.name}"\r\n'
        f"Content-Type: application/x-xpinstall\r\n\r\n".encode() + path.read_bytes() + b"\r\n")
    parts.append(f"--{boundary}--\r\n".encode())
    return b"".join(parts), f"multipart/form-data; boundary={boundary}"


def wait(what, fn):
    deadline = time.time() + TIMEOUT_S
    delay = 3
    while time.time() < deadline:
        result = fn()
        if result is not None:
            return result
        time.sleep(delay)
        delay = min(delay * 1.5, 30)
    sys.exit(f"Timed out after {TIMEOUT_S}s waiting for {what}")


def main():
    if len(sys.argv) != 2:
        sys.exit(__doc__)
    xpi = pathlib.Path(sys.argv[1]).resolve()
    with zipfile.ZipFile(xpi) as z:
        manifest = json.loads(z.read("manifest.json"))
    guid = manifest["browser_specific_settings"]["gecko"]["id"]
    version = manifest["version"]
    print(f"Signing {guid} {version} (unlisted)", file=sys.stderr)

    body, ctype = multipart({"channel": "unlisted"}, "upload", xpi)
    upload = request("POST", "/addons/upload/", body, ctype)
    uid = upload["uuid"]

    def validated():
        u = request("GET", f"/addons/upload/{uid}/")
        if not u.get("processed"):
            return None
        if not u.get("valid"):
            msgs = [m.get("message") for m in (u.get("validation") or {}).get("messages", []) if m.get("type") == "error"]
            sys.exit("Validation failed:\n  " + "\n  ".join(map(str, msgs or [u.get("validation")])))
        return u
    wait("validation", validated)
    print("Validated", file=sys.stderr)

    # PUT addon/<guid> creates the add-on on first use, or adds a version.
    addon = request("PUT", f"/addons/addon/{guid}/", json.dumps({"version": {"upload": uid}}).encode(), "application/json")
    ver = addon.get("latest_unlisted_version") or addon.get("version") or {}
    if ver.get("version") != version:
        # The add-on already existed: the PUT didn't take the version, so add it explicitly.
        ver = request("POST", f"/addons/addon/{guid}/versions/", json.dumps({"upload": uid}).encode(), "application/json")
    vid = ver["id"]

    def signed():
        v = request("GET", f"/addons/addon/{guid}/versions/{vid}/")
        f = v.get("file") or {}
        if f.get("status") == "public" and f.get("url"):
            return f
        if f.get("status") in ("disabled",):
            sys.exit(f"Mozilla rejected version {version}: {v}")
        return None
    f = wait("signing", signed)
    data = request("GET", f["url"], raw=True)
    out = xpi.with_name(f"{guid}-{version}.signed.xpi")
    out.write_bytes(data)
    print(out)
    print("sha256:" + hashlib.sha256(data).hexdigest())


if __name__ == "__main__":
    main()
