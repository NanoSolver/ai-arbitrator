import asyncio
import ipaddress
import socket
from urllib.parse import urlparse

import httpx

MAX_BYTES = 200_000
MAX_CHARS = 20_000


def _assert_public_host(host: str) -> None:
    """Reject hosts that resolve to private, loopback, link-local or otherwise non-public addresses."""
    try:
        infos = socket.getaddrinfo(host, None)
    except socket.gaierror as e:
        raise ValueError(f"Could not resolve host: {host}") from e
    for info in infos:
        ip = ipaddress.ip_address(info[4][0])
        if not ip.is_global:
            raise ValueError("Links to private or internal addresses are not allowed")


async def fetch_url_text(url: str) -> str:
    parsed = urlparse(url)
    if parsed.scheme not in ("http", "https") or not parsed.hostname:
        raise ValueError("Only http(s) URLs are supported")

    await asyncio.to_thread(_assert_public_host, parsed.hostname)

    # Redirects are disabled so a public URL can't bounce us to an internal address.
    async with httpx.AsyncClient(timeout=10, follow_redirects=False) as client:
        async with client.stream("GET", url, headers={"User-Agent": "AI-Arbitrator/0.1"}) as r:
            if r.status_code != 200:
                raise ValueError(f"Link returned HTTP {r.status_code} (use a direct/raw link, not a redirect)")
            ctype = r.headers.get("content-type", "")
            if not (ctype.startswith("text/") or "json" in ctype):
                raise ValueError(f"Unsupported content type: {ctype or 'unknown'}")
            buf = b""
            async for chunk in r.aiter_bytes():
                buf += chunk
                if len(buf) >= MAX_BYTES:
                    break
    return buf[:MAX_BYTES].decode("utf-8", errors="replace")[:MAX_CHARS]
