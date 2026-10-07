#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""Import this library's email templates and landing pages into phish_wu.

Reads manifest.json, assembles the API payloads from the HTML files beside it,
and posts them. Nothing here is phish_wu specific beyond the two endpoints, so
it also works against stock gophish.

    python3 import.py --url https://admin.example.com --key <api key>
    python3 import.py --url https://127.0.0.1:3333 --key <api key> --insecure
    python3 import.py --dry-run

The API key is on the Settings page of the admin interface. Prefer passing it
in the environment rather than on the command line, where it would land in
your shell history:

    PHISH_WU_API_KEY=... python3 import.py --url https://admin.example.com
"""
import argparse
import base64
import json
import os
import ssl
import sys
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))


def read(relative):
    with open(os.path.join(HERE, relative), encoding="utf-8") as handle:
        return handle.read()


def build(manifest):
    """Turn the manifest into (templates, pages) ready to post."""
    templates, pages = [], []
    for item in manifest["items"]:
        template = item["template"]
        attachments = []
        for attachment in template["attachments"]:
            raw = read(attachment["file"]).encode("utf-8")
            attachments.append({
                "name": attachment["name"],
                "type": attachment["type"],
                "content": base64.b64encode(raw).decode("ascii"),
            })
        templates.append({
            "name": template["name"],
            "subject": template["subject"],
            "text": read(template["text"]),
            "html": read(template["html"]),
            "attachments": attachments,
        })

        page = item["page"]
        pages.append({
            "name": page["name"],
            "html": read(page["html"]),
            "capture_credentials": page["capture_credentials"],
            "capture_passwords": page["capture_passwords"],
            "redirect_url": page["redirect_url"],
        })
    return templates, pages


def post(url, key, path, payload, context):
    request = urllib.request.Request(
        url.rstrip("/") + path,
        data=json.dumps(payload).encode("utf-8"),
        method="POST",
        headers={
            "Authorization": "Bearer " + key,
            "Content-Type": "application/json",
        },
    )
    try:
        with urllib.request.urlopen(request, context=context) as response:
            return True, json.loads(response.read().decode("utf-8"))
    except urllib.error.HTTPError as error:
        body = error.read().decode("utf-8", "replace")
        try:
            return False, json.loads(body).get("message", body)
        except ValueError:
            return False, body
    except urllib.error.URLError as error:
        return False, str(error.reason)


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--url", default=os.environ.get("PHISH_WU_URL", ""),
                        help="admin interface base URL, e.g. https://admin.example.com")
    parser.add_argument("--key", default=os.environ.get("PHISH_WU_API_KEY", ""),
                        help="API key (Settings page). PHISH_WU_API_KEY is preferred.")
    parser.add_argument("--insecure", action="store_true",
                        help="skip TLS verification, for the self-signed certificate on loopback")
    parser.add_argument("--dry-run", action="store_true",
                        help="assemble and report the payloads without sending anything")
    parser.add_argument("--only", default="",
                        help="comma separated ids to import, e.g. 01,04,08")
    args = parser.parse_args()

    manifest = json.loads(read("manifest.json"))
    if args.only:
        wanted = {part.strip() for part in args.only.split(",")}
        manifest["items"] = [i for i in manifest["items"] if i["id"] in wanted]
        if not manifest["items"]:
            sys.exit("no scenarios matched --only " + args.only)

    templates, pages = build(manifest)

    if args.dry_run:
        print("%d templates, %d landing pages" % (len(templates), len(pages)))
        for template, page in zip(templates, pages):
            attachments = ", ".join(a["name"] for a in template["attachments"]) or "-"
            print("  %-42s html=%6d  attachments=%s"
                  % (template["name"], len(template["html"]), attachments))
            print("  %-42s html=%6d  capture_passwords=%s"
                  % (page["name"], len(page["html"]), page["capture_passwords"]))
        return

    if not args.url or not args.key:
        sys.exit("need --url and --key (or PHISH_WU_URL and PHISH_WU_API_KEY)")

    context = None
    if args.insecure:
        context = ssl.create_default_context()
        context.check_hostname = False
        context.verify_mode = ssl.CERT_NONE

    failures = 0
    for path, items in (("/api/templates/", templates), ("/api/pages/", pages)):
        for payload in items:
            ok, detail = post(args.url, args.key, path, payload, context)
            if ok:
                print("  ok      %s" % payload["name"])
            else:
                failures += 1
                print("  FAILED  %s: %s" % (payload["name"], detail))

    total = len(templates) + len(pages)
    print("%d of %d imported" % (total - failures, total))
    if failures:
        sys.exit(1)


if __name__ == "__main__":
    main()
