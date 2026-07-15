# Mechanism Shortlist Reviewer Receipt — Independent Sonnet 5 Reviewer

> Document hash: `910b58a460aeac52d119007e1c250475ab9390e15c38c5f0d853ed4992bca829`

Binds `mechanism_shortlist_reviewer_receipt.json` (`hashes.receipt_hash`). Reviews `candidates_opus.json`, `candidates_fable.json`, `candidates_grok.json`, `mechanism_hypothesis_library.json`, `mechanism_shortlist.json`. Independent reviewer subagent, distinct session from all three candidate owners. No commits; no code/plan/master/records/private-raw/G1-G10 modification; no outcome field read.

## Independent methods (M1–M5)

- **M1 — upstream hash reverification**: initial review found **6/7 reproduced exactly** and `candidates_fable.json`'s then-declared `f6af94cd...270e` did not reproduce; independent recompute was `a244b53c...f97cea`.
- **M2 — Markdown document-hash cross-check**: standalone Python recompute of all three candidate `.md` files' `SELF_EXCLUDED`-normalized SHA-256.
- **M3 — feature-side local history reverification**: direct sampling of `records/history/okx_1h` and `records/history/okx_funding` (price/funding-rate/timestamp fields only). Corroborated file counts and date windows; found `rubik_lsratio_1h` (missed by Opus's OI scan); found zero local calendar artifacts; found the `okx_1h` universe spans multiple distinct calendars/asset classes; independently ran and **passed** FAB-01's falsification cut #1 (perp does not freeze during underlying closure, sampled Saturday, AAPL).
- **M4 — cross-owner contradiction search**: read all three owner files together (none of them did). Found Fable's own hard-reject of "Rubik long/short-ratio extreme" as a public factor directly undercuts OPUS-01's only currently-dense decision-time crowding proxy — decisive against shortlisting OPUS-01.
- **M5 — G-series homology grounding**: grepped `FIRST_BATTLE_MASTER_PLAN.md` §4.2 (read-only) for G2's actual frozen protocol rather than trusting each owner's self-declared non-isomorphism proof; confirmed G2 = 162-combination BTC-only funding-state grid, 0 nominations.
- **M6 — Fable owner hash-correction reverification**: standalone Go canonicalizer in `/tmp` independently recomputed corrected `candidates_fable.json` as `a244b53c...f97cea`, exactly matching both the new declaration and the prior independent recompute; Python independently recomputed corrected `candidates_fable.md` as `879d93eb...703a9`. Status: **`RESOLVED_OWNER_HASH_CORRECTED`**. Identical pre/post canonical JSON hash proves no mechanism-content change apart from the self-hash field; scores and shortlist are unchanged.

## Conflict matrix (summary)

| Topic | Conflict found? | Resolution |
|---|---|---|
| Funding-settlement decision-time crowding feature | **Yes** (cross-owner) | OPUS-01 + Fable's own hard-reject jointly falsify a non-public-factor path → RETAIN_NOT_SHORTLISTED |
| Funding clamp/pin saturation | No | Compatible views (different blockers); both rejected |
| Borrowing-interest forced-repay identity | No | OPUS-03/GROK-02 agree; both rejected |
| Listing/delisting forced flow | No (different lifecycle ends) | FAB-02 downgraded further than owner's own framing per task's specific scrutiny instruction; GROK-03 rejection affirmed |
| Mark/index dislocation vs. `sparse_forced_flow` | No | Both owners self-admit existing-route homology; affirmed |
| Cross-venue ops asymmetry vs. existing route | No | GROK-07 self-admits duplication; treated as not-novel |
| Equity-perp session gap | No (sole owner) | Reviewer materially extends/corrects Fable's own framing (PIT overstatement, moat gap, one falsification cut independently passed) → conditional shortlist, not a straight adoption |

Full row-level detail is in the JSON.

## Input hashes

| Artifact | Declared | Reverified |
|---|---|---|
| `candidates_opus.json` | `1ed962fc...c4d32` | ✅ |
| `candidates_fable.json` | `a244b53c...f97cea` | ✅ — `RESOLVED_OWNER_HASH_CORRECTED` |
| `candidates_fable.md` | `879d93eb...703a9` | ✅ |
| `candidates_grok.json` | `615c1e5a...dddc` | ✅ |
| `route_data_contracts.json` | `4a4c201d...3588` | ✅ |
| `route_data_source_registry.json` | `1c667256...52e5318` | ✅ |
| `p0_swap_data_fitness_reviewer_receipt.json` | `0cdd322c...dcad4` | ✅ |
| `data_mainline_closure_v1.json` | `d4f78f1e...568525e` | ✅ |
| `history_catalog.json` | `f41ed258...316379c` (referenced, not a self-excluded envelope) | Read via targeted grep/offset, not a full hash recompute |

## Scoring hash

`scoring_hash.scoring_input_hash` = `mechanism_hypothesis_library.json`'s own `hashes.library_hash` (`d208f96c...10eae5`). The shortlist binds that library hash and has `hashes.shortlist_hash=b3c63aee...15c541`; this receipt binds both.

## Verdict

**`PASS_INDEPENDENT_MECHANISM_TRIAGE_ONE_CONDITIONAL_SHORTLIST_ENTRY`**

### Approved
- The 19-candidate triage/clustering/scoring in `mechanism_hypothesis_library.json`.
- The single conditional shortlist entry in `mechanism_shortlist.json`, with its rescoping and open falsification cuts treated as binding.
- The resolved Fable hash-integrity finding: corrected JSON/Markdown hashes now independently reproduce; canonical candidate content, scores, and shortlist are unchanged.

### Not approved
- Any data-contract freeze, download, or acquisition beyond what is already local plus the two zero-cost public reference lookups.
- Any Phase A build, generation preregistration, validation/WF, exam, shadow, or real-money action.
- Treating the shortlist as proof the mechanism is real — it is a conditional observability/plausibility pass with two falsification cuts still open.
- Treating any owner's self-declared classification/score/non-isomorphism proof as sufficient on its own.

### If verification had found zero eligible routes
18 of 19 candidates independently failed regardless of scoring; the one surviving entry rests on a rescoping and two open cuts this reviewer performed/identified but did not fully close. This was a close call, not a clean one; a lower-confidence outcome would have correctly been 0.

## Hash and verification methods

- **JSON**: `hashes.receipt_hash` set to null, then `datatruth.HashJSON`.
- **Markdown**: normalize the `> Document hash:` line to `` `SELF_EXCLUDED` ``, then SHA-256 over UTF-8 LF bytes.

## Safety

`system_status=NO_GO`, `trade_allowed=false`, `labels_unblinded=false`, `routes_started=0`, `routes_reopened=0`. No commits made.
