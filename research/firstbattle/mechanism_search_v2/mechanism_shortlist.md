# Mechanism Shortlist (机制入选清单) — Independent Sonnet 5 Reviewer

> Document hash: `4ac6615030f9c56f58b74eae15defbd2bf9b5214daf189bb1a49b5875f92486e`

Binds `mechanism_shortlist.json` (`hashes.shortlist_hash`) to `mechanism_hypothesis_library.json` (`hashes.library_hash=d208f96cbf6b22623df47858ab9a0aaaa46d80dd16768ad993daff02bc10eae5`). Max allowed: 3 non-homologous routes; 0 is an explicitly allowed outcome. **Selected: 1.** The Fable owner's pure self-hash correction changed no candidate content, score, or shortlist decision.

## Honesty statement

Of 19 candidates, only **FAB-MSV2-01, rescoped**, satisfies all five frozen conditions (existing-history complete outcome-blind denominator, event identity, ≥50 events/≥20 days/≥10 blocks *possible*, non-pure-public-factor, non-G-series-isomorphic) at a "could plausibly reach the gate" level — not a full validation pass. This is a close, conditional call with two open falsification cuts, not a clean one. If a stricter bar is wanted, 0 is the defensible alternative.

## Not selected

| Candidate | Reason |
|---|---|
| MSV2-OPUS-01 | Decision-time OI/crowding feature cannot be built at complete-denominator density without collapsing into a public factor (the same Rubik long/short-ratio pattern a different owner independently hard-rejected); family-adjacent to G2's already-exhausted funding-state grid. |
| FAB-MSV2-06 | Owner's own highest self-flagged G1-isomorphism risk; weakest moat (institutional cross-venue carry desks are the natural operator); 1h bars likely too coarse. |
| FAB-MSV2-04 | Measured support (~a dozen events/4 symbols) is an order of magnitude short of the 50-event gate. |
| FAB-MSV2-02 | No directly observable forced-seller identity; owner frames payer as an unresolved symmetric hypothesis; own counterexample is generic listing drift. |
| All other 14 | Hard-rejected on complete-denominator-not-constructible and/or PIT-backfill and/or existing-route/G-series homology. |

## Shortlisted route #1 — `equity_perp_underlying_session_gap_forced_repricing` (from FAB-MSV2-01, rescoped)

**One line**: OKX tokenized-stock perpetuals trade 24/7 while their cash-market underlying trades on a fixed exchange calendar; at each close/reopen boundary, the wrong-direction leveraged perp holder is forced to mark-to-market against the new reference. Rescoped to the single calendar cluster this review could verify.

**Why rescoped**: the original 121-symbol universe spans several distinct calendars (US NYSE/NASDAQ, foreign single stocks/country ETFs on other exchanges, commodities with no clean session boundary, private-company perps with no underlying market) and no local calendar artifact exists for any of them. **Rescoped universe: US-listed common stocks/ETFs on the NYSE/NASDAQ calendar only.** Explicitly excluded for now: foreign country ETFs/single stocks, commodities, private-company perps.

**Payer**: the perp position holder opposite the reopening reference price, forced into mark-to-market/margin consequences at reopen (not a voluntary decision); secondary payer = the market maker re-centering inventory at reopen. Independently confirmed the perp does **not** freeze during closure (sampled Saturday, AAPL, continuous hourly price/volume movement) — necessary, not sufficient.

**Support status**: `OBSERVABLE_ON_EXISTING_LOCAL_HISTORY_PENDING_CALENDAR_CONSTRUCTION_AND_TWO_OPEN_FALSIFICATION_CUTS` — explicitly **not** a DATA_PARTIAL auto-include. Zero new OKX acquisition needed for the raw denominator; ≥50 events/≥20 days/≥10 blocks is plausible for the US-only subset from tape already on disk.

**Action branches**: unchanged from the original candidate — default `NO_TRADE` during closure; pre-close and reopen branches bucket by observable basis with no presupposed winner direction (left for Phase B).

**Source requirements / minimal data**: zero new OKX acquisition (reuse existing `okx_1h`/`okx_funding`/instruments snapshot). New requirement: construct and hash-bind a public NYSE/NASDAQ trading calendar (holidays/early closes/DST) and locate OKX's public documentation on index/mark methodology during underlying-closed sessions — both zero-cost, zero-purchase, zero-bulk-download.

**Complete denominator / matched loser**: every (symbol × session boundary) event for the rescoped US subset, traded or not; matched loser = same-symbol/same-boundary-type events with opposite/near-zero basis plus same-day cross-symbol controls; excluded non-US clusters logged separately to prevent multi-calendar contamination. Independence caveat carried forward: effective independent sample ≈ trading-day count (~90 in the current local window), not raw event count.

**Strongest counterexample**: overnight/weekend equity gap is one of the most published anomalies in finance; the moat argument only excludes *regulated* institutions, not crypto-native market makers who already quote this exact perp 24/7 — independently identified as the weakest link. If OKX freezes mark/index during closure (unconfirmed), the mechanism collapses to a single-tick jump no small taker can be positioned inside.

**Budget/stop**: 1 CPU / 6h / 1 attempt / zero new OKX acquisition. Stop gates: mark/index found frozen during closure → close immediately; boundary vs. non-boundary features not separable → UNIDENTIFIABLE close; unverifiable calendar coverage for a sub-period → exclude that period, never fabricate; decision feature collapses to a public crowding factor → ISOMORPH_PUBLIC_FACTOR close.

**Why not an ordinary institutional strategy**: regulated equity institutions are legally barred from this offshore derivative; crypto-native institutions are not barred and this argument is left explicitly unverified — the only remaining (unverified) claim is that per-symbol OI caps and thin reopen-window depth bound exploitable capacity to roughly individual-account scale.

**Phase A prerequisites (binding, not advisory)**: construct/hash-bind the NYSE/NASDAQ calendar; locate/hash-bind OKX's index/mark documentation; enumerate the exact US-only symbol subset with exclusions logged; re-run the movement-during-closure check across the full subset; run the boundary-vs-non-boundary feature-separability check — all before any Phase A data-contract freeze.

## Hash and verification methods

- **JSON**: `hashes.shortlist_hash` set to null, then `datatruth.HashJSON`.
- **Markdown**: normalize the `> Document hash:` line to `` `SELF_EXCLUDED` ``, then SHA-256 over UTF-8 LF bytes.

## Stop decision

This file freezes shortlist candidate cards only. It does not authorize any data-contract freeze, download, Phase A build, validation/WF, exam, shadow, or real-money action.
