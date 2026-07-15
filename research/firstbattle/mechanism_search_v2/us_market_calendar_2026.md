# US Market Calendar 2026 (NYSE + Nasdaq regular session)

> Document hash: `5a094adef662ca2b3136e0452e4b5eb79020f88a1aad668045e2a88583e30e0b`
> JSON artifact: `us_market_calendar_2026.json`
> JSON hash (`hashes.calendar_hash`): `65e7795a7a68d0384ddad15188d8c8b2818df225d042197b4d12ee7a51086297`
> Bound shortlist hash: `b3c63aee84509eead9dc24bf067e68cdc16f08f50a532e006a68e633b015c541`
> Bound reviewer receipt hash: `6c5a0c25f065e169f64f909aacedd4fc85f7ca0dad066222cc5e3f146a2bbdce`
> KnownFrom: `2026-07-14T06:20:31Z`

## Scope
America/New_York **regular/core** cash-equity session for **2026** only. Pre/after-hours excluded from the open window.

Both **NYSE** and **Nasdaq** official calendars were fetched independently; holiday and early-close dates **agree**. This is not a silent NYSE overlay onto Nasdaq.

## Regular hours
| Venue | Regular session (ET) | Source class |
| --- | --- | --- |
| NYSE Core Trading Session | 09:30–16:00 | OFFICIAL_EXCHANGE |
| Nasdaq Stock Market | 09:30–16:00 | OFFICIAL_EXCHANGE |
| Early close (both) | 09:30–13:00 | OFFICIAL_EXCHANGE |

## 2026 full holidays (both venues)
2026-01-01, 2026-01-19, 2026-02-16, 2026-04-03, 2026-05-25, 2026-06-19, 2026-07-03 (Independence Day observed), 2026-09-07, 2026-11-26, 2026-12-25.

## 2026 early closes (1:00 p.m. ET, both venues)
2026-11-27 (day after Thanksgiving), 2026-12-24 (Christmas Eve).

## DST
- Starts 2026-03-08 (second Sunday in March) → open UTC shifts 14:30Z → 13:30Z
- Ends 2026-11-01 (first Sunday in November) → open UTC shifts back to 14:30Z
- Per-day `open_utc`/`close_utc`/`dst_in_effect` live in the JSON session table (365 rows).

## Official sources (hashes at fetch)
See JSON `official_sources[]` for URL, fetched_at_utc, content_sha256.

Third-party aggregators and the ICE IR press URL (HTTP 403) were **not** used as evidence.

## PIT
KnownFrom = earliest successful official HTML fetch in the lookup batch. Do not backfill pre-KnownFrom rule changes from this snapshot.
