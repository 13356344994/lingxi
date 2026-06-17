"""Binance public API fetch with disk cache, pagination, and rate-limit backoff."""

from __future__ import annotations

import hashlib
import json
import time
from pathlib import Path
from typing import Any
from urllib.parse import urlencode

import pandas as pd
import requests

CACHE_DIR = Path(__file__).resolve().parent / "cache"
SPOT_BASE = "https://api.binance.com"
FUTURES_BASE = "https://fapi.binance.com"

REQUEST_TIMEOUT = 30
MIN_REQUEST_INTERVAL = 0.12  # ~8 req/s
MAX_RETRIES = 6

_last_request_at = 0.0


def _cache_path(key: str) -> Path:
    digest = hashlib.sha256(key.encode()).hexdigest()[:32]
    return CACHE_DIR / f"{digest}.json"


def _load_cache(key: str) -> Any | None:
    path = _cache_path(key)
    if not path.exists():
        return None
    with path.open() as f:
        return json.load(f)


def _save_cache(key: str, payload: Any) -> None:
    CACHE_DIR.mkdir(parents=True, exist_ok=True)
    path = _cache_path(key)
    with path.open("w") as f:
        json.dump(payload, f)


def _throttle() -> None:
    global _last_request_at
    now = time.time()
    wait = MIN_REQUEST_INTERVAL - (now - _last_request_at)
    if wait > 0:
        time.sleep(wait)
    _last_request_at = time.time()


def _retry_delay(attempt: int, status: int | None) -> float:
    base = 2.0
    if status == 202:
        base = 4.0
    elif status == 429:
        base = 6.0
    elif status in (403, 418):
        base = 8.0
    elif status in (502, 503, 504):
        base = 3.0
    return min(20.0, base * (attempt + 1))


def http_get_json(url: str, cache_key: str | None = None) -> Any:
    key = cache_key or url
    cached = _load_cache(key)
    if cached is not None:
        return cached

    last_err: Exception | None = None
    for attempt in range(MAX_RETRIES):
        _throttle()
        try:
            resp = requests.get(url, timeout=REQUEST_TIMEOUT)
            if resp.status_code == 200:
                payload = resp.json()
                _save_cache(key, payload)
                return payload
            if resp.status_code in (202, 429, 403, 418, 502, 503, 504):
                time.sleep(_retry_delay(attempt, resp.status_code))
                continue
            resp.raise_for_status()
        except requests.RequestException as exc:
            last_err = exc
            time.sleep(_retry_delay(attempt, None))
    raise RuntimeError(f"GET failed after retries: {url}") from last_err


def verify_dual_listed(symbols: list[str]) -> tuple[list[str], list[str]]:
    """Return (valid, rejected) symbols with both spot and USDT-M perp on Binance."""
    spot_info = http_get_json(f"{SPOT_BASE}/api/v3/exchangeInfo", "spot_exchange_info")
    fut_info = http_get_json(f"{FUTURES_BASE}/fapi/v1/exchangeInfo", "fut_exchange_info")

    spot_set = {
        s["symbol"]
        for s in spot_info.get("symbols", [])
        if s.get("status") == "TRADING" and s.get("quoteAsset") == "USDT"
    }
    fut_set = {
        s["symbol"]
        for s in fut_info.get("symbols", [])
        if s.get("status") == "TRADING" and s.get("contractType") == "PERPETUAL" and s.get("quoteAsset") == "USDT"
    }

    valid, rejected = [], []
    for sym in symbols:
        if sym in spot_set and sym in fut_set:
            valid.append(sym)
        else:
            rejected.append(sym)
    return valid, rejected


def fetch_funding_history(symbol: str, start_ms: int, end_ms: int) -> pd.DataFrame:
    rows: list[dict[str, Any]] = []
    cursor = start_ms
    page = 0
    while cursor < end_ms:
        params = {
            "symbol": symbol,
            "startTime": cursor,
            "endTime": end_ms,
            "limit": 1000,
        }
        url = f"{FUTURES_BASE}/fapi/v1/fundingRate?{urlencode(params)}"
        key = f"funding:{symbol}:{cursor}:{end_ms}"
        batch = http_get_json(url, key)
        if not batch:
            break
        for item in batch:
            rows.append(
                {
                    "funding_time": int(item["fundingTime"]),
                    "funding_rate": float(item["fundingRate"]),
                }
            )
        last_ts = int(batch[-1]["fundingTime"])
        next_cursor = last_ts + 1
        if next_cursor <= cursor:
            break
        cursor = next_cursor
        page += 1
        if len(batch) < 1000:
            break

    if not rows:
        return pd.DataFrame(columns=["funding_time", "funding_rate"])

    df = pd.DataFrame(rows).drop_duplicates("funding_time").sort_values("funding_time")
    return df.reset_index(drop=True)


def _fetch_klines_paginated(
    base_url: str,
    symbol: str,
    interval: str,
    start_ms: int,
    end_ms: int,
    prefix: str,
) -> pd.DataFrame:
    rows: list[list[Any]] = []
    cursor = start_ms
    interval_ms = _interval_to_ms(interval)

    while cursor < end_ms:
        params = {
            "symbol": symbol,
            "interval": interval,
            "startTime": cursor,
            "endTime": end_ms,
            "limit": 1500,
        }
        url = f"{base_url}?{urlencode(params)}"
        key = f"{prefix}:{symbol}:{interval}:{cursor}:{end_ms}"
        batch = http_get_json(url, key)
        if not batch:
            break
        rows.extend(batch)
        last_open = int(batch[-1][0])
        next_cursor = last_open + interval_ms
        if next_cursor <= cursor:
            break
        cursor = next_cursor
        if len(batch) < 1500:
            break

    if not rows:
        return pd.DataFrame(columns=["open_time", "close"])

    df = pd.DataFrame(
        rows,
        columns=[
            "open_time",
            "open",
            "high",
            "low",
            "close",
            "volume",
            "close_time",
            "quote_volume",
            "trades",
            "taker_buy_base",
            "taker_buy_quote",
            "ignore",
        ],
    )
    df["open_time"] = df["open_time"].astype("int64")
    df["close"] = df["close"].astype(float)
    return df.drop_duplicates("open_time").sort_values("open_time").reset_index(drop=True)


def _interval_to_ms(interval: str) -> int:
    unit = interval[-1]
    n = int(interval[:-1])
    mult = {"m": 60_000, "h": 3_600_000, "d": 86_400_000}[unit]
    return n * mult


def fetch_spot_klines(symbol: str, start_ms: int, end_ms: int, interval: str = "1h") -> pd.DataFrame:
    return _fetch_klines_paginated(
        f"{SPOT_BASE}/api/v3/klines",
        symbol,
        interval,
        start_ms,
        end_ms,
        "spot_klines",
    )


def fetch_mark_klines(symbol: str, start_ms: int, end_ms: int, interval: str = "1h") -> pd.DataFrame:
    return _fetch_klines_paginated(
        f"{FUTURES_BASE}/fapi/v1/markPriceKlines",
        symbol,
        interval,
        start_ms,
        end_ms,
        "mark_klines",
    )
