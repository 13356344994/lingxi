# History downloader owner journal

## Problem restatement

Build only a local-testable, GET-only OKX history acquisition path that routes
data into bounded private/public storage outside the repository. It must
fail closed on unsupported endpoints, credentials, pagination state, and
repository output paths. This is data engineering only: no execution,
profitability research, or live connection is performed in this change.

## Competing hypotheses

1. The existing downloader is nearly complete and only stale tests need fixing.
2. The existing downloader has safety primitives, but its state transitions and
   CLI do not yet implement a usable paginated acquisition workflow.
3. The dominant risk is a boundary failure: credential cross-use, generic HTTP,
   raw IDs in research output, or an ambiguous `after` cursor rather than a
   simple compile failure.

## Strongest counterexamples and blind spots

* A successful two-page fixture does not prove real OKX cursor semantics; the
  endpoint-specific cursor must be derived from the last `ordId`/`tradeId` and
  later red-teamed against documented production responses.
* A legal future OKX field (including `fillPnl`) must not stop raw capture, but
  silently ignoring it without a field-presence receipt would hide schema
  change.
* A test server cannot establish that a real key has no trade permission or
  that an upstream archive URL remains official.

## Chosen approach

Keep a narrow request type and the fixed `www.okx.com` HTTPS client. Make the
credential loader accept only `okx: {api, secret, passphrase}`. Treat raw data
as immutable private/public evidence; project only endpoint allowlisted fields,
tokenize IDs, and record omitted-field names/counts without values. Bind
checkpoints to endpoint/query/schema/output paths, use atomic writes, and
resume only sealed matching state. Expose offline CLI modes for private
pagination, instrument snapshots, and a one-day/one-instrument archive smoke.

## Rejected alternatives

* Reusing venue adapters: rejected because it enlarges the execution boundary.
* A generic URL/method client: rejected because it can escape the GET allowlist.
* Failing a full page for unknown upstream fields: rejected because it makes
  legitimate upstream schema expansion destroy evidence capture.
* Permitting flat credentials in production for compatibility: rejected because
  it can cross-wire another venue's top-level credentials.

## Independent review feedback (2026-07-13)

The first implementation passed local tests but those tests missed recovery and
evidence-binding failures. In particular, a `Done` checkpoint could precede its
manifest, identity-less records were silently dropped, the disk cap measured
only raw pages, and archive smoke accepted caller-labelled ZIPs without proven
discovery provenance. A tracked `0644` credential file was also incorrectly
treated as a hard error even though permissions and key capabilities are audit
warnings, not data-source eligibility gates.

## Updated competing hypotheses

1. Atomic file writes are sufficient for crash recovery.
2. A sealed state machine must bind every critical configuration input and
   consider completion valid only after a sealed manifest is re-read and
   verified.
3. Upstream labels and required identifiers can be trusted because raw evidence
   remains available.
4. Any unproven archive-discovery schema makes connected archive smoke
   unavailable; host allowlisting alone cannot establish instrument/date
   provenance.

The strongest counterexample to hypothesis 1 is a crash after saving `Done` but
before publishing the manifest. The strongest counterexample to hypothesis 3 is
a record without `ordId`/`tradeId`: silently removing it changes the research
denominator. Therefore hypothesis 2 is adopted, identity absence fails closed,
duplicates are counted, and hypothesis 4 disables connected archive smoke until
official discovery semantics are independently authenticated.

## Revised design decisions

Credential permissions and trade capability produce value-free warnings that
are emitted to stderr and bound into manifests. Checkpoints bind downloader
version, privacy, cap, raw/projection/manifest paths, and token-salt identity.
Completion transitions through `active -> pending_manifest -> published`; a
published resume must verify both manifest integrity and its bound hash.
Checkpoint collections have explicit limits, and total on-disk managed bytes
are measured before page acceptance. Event and receive clocks are tracked as
ranges with explicit fitness warnings. Instrument snapshots publish a
hash-bound manifest. Connected archive smoke remains unavailable pending a
verified discovery schema.

## Sonnet red-team BLOCK feedback (2026-07-14)

The prior boundary was still wrong: “private checkpoint” was treated as
permission to persist exchange identifiers. Cursor chains, resume cursors, and
dedup keys therefore leaked raw `ordId`/`billId`/`tradeId` outside immutable
private raw responses. Contract validation also authenticated only the contract
itself, repository detection depended on directory depth, pagination lacked
numeric monotonicity, and endpoint-local caps did not bound the whole download
root.

### Changed competing hypotheses

1. Private filesystem permissions are sufficient protection for raw IDs.
2. Only immutable private raw responses may retain raw IDs; all derived state,
   including checkpoints, must use stable HMAC tokens.
3. Persisting the raw resume cursor is necessary for reliable recovery.
4. The raw cursor can be reconstructed in memory from the last hash-verified raw
   page, while the persisted loop/dedup state remains tokenized.

Hypotheses 2 and 4 are adopted. A complete closure requires a multi-page crash
fixture proving the resumed HTTP `after` value is reconstructed correctly while
checkpoint/manifest bytes contain none of the fixture IDs. Pagination must also
be strictly decreasing decimal order (`ordId` for orders, `billId` for fills)
within and across pages.

### Additional closure conditions

* Repository identity requires both `.git` and `go.mod` with module `lingxi`;
  the canonical contract path is fixed under that repository, and canonical or
  not-yet-created raw roots inside it are rejected.
* Every uniquely reproducible upstream binding is recomputed with its declared
  self-exclusion rule. A binding whose authoritative content hash requires
  forbidden/unavailable source material is an explicit connection blocker, not
  a skipped check.
* The whole historydownload root has one hard cap, archive extraction validates
  in a staging directory before atomic publication, and unused account/config
  HTTP capability is removed.
* Red-team closure requires target, race, vet, formatting, diff, forbidden
  surface, raw-ID byte-scan, monotonicity, symlink-boundary, binding-tamper, and
  archive rollback tests. Network remains disabled until all reproducible
  bindings pass and any declared blocker is independently resolved.

## Second Sonnet BLOCK and narrowed authority model

The remaining mistake was claiming a canonical path and a pre-write size scan
were sufficient while an attacker or second process could swap the root, race
salt creation, or consume capacity between checks. Snapshot publication also
updated three authoritative files independently.

The implementation now deliberately drops unnecessary flexibility:

1. Connected CLI runs may use only the mode-specific hard-coded default root.
2. A validated root-owned `flock` is held from salt initialization through final
   publication; library entry points require proof of that held lock.
3. Salt creation is exclusive and cached only after validating regular-file,
   owner, mode, and exact-length invariants.
4. Instrument snapshots publish immutable generations and atomically switch one
   hash-sealed current pointer only after every generation artifact is synced.

The counterexample that closes the earlier hypothesis is two processes both
passing an unlocked capacity check and then writing. Therefore no unlocked
write or arbitrary connected root remains authorized. The history catalog
binding is checked structurally from its frozen nested file hashes, line counts,
entry set, aggregate counts, schema hash, and declared data hash without
opening `records/`. Closure still requires contention tests, adversarial salt
metadata tests, fault injection at every snapshot publication boundary, and a
new independent red-team verdict; this journal does not claim self-approval.

## Frozen public archive P0 smoke

The public archive path is intentionally one UTC day by one exact instrument.
Competing hypotheses are:

1. `instIdList` guarantees the discovery response contains a dedicated
   instrument file.
2. SWAP discovery may ignore `instIdList` and return an `allswap-*` market-wide
   package.
3. A caller-supplied static URL is equivalent to discovery provenance.

Hypothesis 2 is the strongest counterexample and is treated as a hard STOP:
any `allswap`/`allspot` evidence is rejected before the ZIP transport is called.
Hypothesis 3 is rejected; the CLI has no archive URL flag. The selected entry
must be the unique nested discovery item whose parent instrument/type, `dateTs`,
filename product/date/instrument, official static HTTPS URL basename, and
declared size all bind the frozen request.

The smoke authority uses the same locked, immutable-generation model as
instrument snapshots. Discovery JSON, ZIP, normalized single CSV, receipt, and
manifest are hash-bound before an atomic current-pointer switch. This proves
only decoding/provenance for that file. It does not prove rule PIT, original
collector receive time, full-market coverage, or permission to add bulk,
multi-day, caller-URL, or list modes.

## Public archive Sonnet BLOCK refinement

The first archive implementation still accepted an exact match while silently
ignoring extra discovery scope, advertised unverified book products, reserved
capacity with a coarse multiplier instead of checking each actual write, and
reported the requested day as though it were the observed CSV event range.

The revised competing hypotheses are:

1. Filtering one matching entry is safe even when unrelated details/files are
   present.
2. Any extra data group, detail, groupDetail, day, instrument, or file proves
   the response scope is broader than the frozen smoke and must STOP.
3. A requested one-day window is equivalent to the observed event-time range.
4. The receipt must separately retain the requested bounds and the actual
   min/max timestamps scanned from every CSV row.

Hypotheses 2 and 4 are adopted. Production archive smoke now permits only module
`1` trades; modules `4` and `5` remain metadata-investigation subjects until a
new generation freezes real exact book samples. Redirects are rejected by a
cloned HTTP client before a second request can carry headers. Root capacity is
checked under the root lock before discovery bytes and before each streamed ZIP
or CSV chunk, then again for receipt, manifest, and current-pointer writes.

## P0 SWAP Data Fitness generator closure (2026-07-14)

The hand-counted Data Fitness artifact was invalidated after cross-endpoint
instrument drift proved that a complete token join did not establish consistent
exchange identity. The replacement calculator reads only published tokenized
projection pages and the public instrument generation; it has no raw or network
surface and does not update the frozen owner artifact.

The strongest remaining evidence counterexample is that source manifests bind
raw pages, not projection bytes. Therefore the receipt does not claim
raw/projection equivalence. It records
`PROJECTION_BYTES_FROZEN_AT_MEASUREMENT` and binds each ordered projection page
by relative filename, SHA-256, byte length, and row count, plus the verified
source manifest hash. Row totals must equal manifest totals.

Outcome blindness is fail-closed over the frozen forbidden vocabulary after
case and separator normalization, both in manifest projection fields and every
projected row. Page names must be contiguous, each page must contain exactly
one JSON value, timestamps are exact positive decimal milliseconds, and fill
trade/bill tokens are required and globally unique. Receipt hashing excludes
only its own hash field. CLI publication is restricted to the canonical
repository `research/` tree and uses atomic replacement. This is an owner
measurement implementation, not independent approval or route reopening.

The second review showed that an outcome-name check alone was not a complete
projection firewall: an extra harmless-looking key, nested object, JSON array,
or non-string scalar could bypass the intended endpoint contract. Calculator
v3 therefore requires each verified manifest schema and schema hash to equal
the code-owned `ProjectionSchema(endpoint)`, recursively scans all nested keys
before rejecting every non-scalar value, and requires every row to contain
exactly the endpoint fields except documented optional token fields.

It also rejects descriptive-count false greens: `accFillSz` is a strict,
present, non-negative decimal where only exact zero is no-fill; order size and
instrument/fill size-price fields have explicit positive/non-negative gates.
The CLI no longer accepts an arbitrary research path. Its only writable target
is the canonical repository
`research/firstbattle/p0_swap_data_fitness_measurement.json`, resolved through
the frozen contract repository anchor and rejected on symlink or cwd escape.

## Report Center inspector authority closure

The first manual-CSV inspector trusted a short list of lower-cased headers and
an atomic current write. Sonnet's blocking counterexamples showed that
`Realized PnL`, `fill_pnl`, `futureReturn`, and `unrealizedPnl` could be mapped
onto innocent destinations, while current and receipt did not authenticate all
generation artifacts.

The revised boundary adopts source-and-destination checks: Unicode-aware,
punctuation-free source canonicalization and sensitive-token containment run at
mapping load, binding, and projection. Unmapped outcome columns expose only
canonical names and header presence, never cell values. The CLI accepts only
basenames in fixed owner-only `inbox/` and `mappings/` directories, opened
without following symlinks; raw bytes are hashed before schema interpretation.

Publication preserves an existing current through every injected failure. A
self-hashed current leads to an owner-0700 generation containing an exact
mapping snapshot, projection, checkpoint, and self-hashed receipt.
`VerifyCurrent` enforces the artifact whitelist, re-hashes every artifact, and
re-hashes the external inbox CSV reference. Event-time ranges are observed per
canonical field and accept only 10/13-digit epochs or timezone-qualified
RFC3339. PASS remains `MANUAL_EXPORT_UNVERIFIED`,
`ORIGINAL_RECEIVE_TIME_ABSENT`, and
`INCOMPLETE_ATTEMPTS_DENOMINATOR`; it grants neither PIT nor a complete
attempts denominator.

## Binance USD-M async order-history pilot (offline only)

### Competing hypotheses

1. The current USD-M endpoints `GET /fapi/v1/order/asyn` and
   `GET /fapi/v1/order/asyn/id` remain documented signed GET endpoints with a
   bounded `startTime`/`endTime` range and a download-status schema compatible
   with the isolated reader.
2. A GET request that creates an asynchronous report job is free of meaningful
   side effects and can be retried freely.
3. A download URL returned by the status endpoint can be trusted solely because
   it appeared in a signed API response.
4. A 7-day order export that contains no zero-fill cancelled/expired records
   establishes that they are not retained by Binance.

Hypotheses 2--4 are rejected. The request receipt explicitly records the async
job/quota side effect, returned download URLs require a direct HTTPS official
allowlist with redirects disabled, and an empty/non-zero-fill window is a STOP
result rather than evidence about any other period. Hypothesis 1 is deliberately
unresolved: implementation contains only an offline, fail-closed target until
official current documentation and an independent Sonnet review establish the
endpoint method, parameters, signing, quota, and response schemas.

### Independent Sonnet review result

The initial offline implementation was blocked because its declared root cap,
lock, generation/current/hash/checkpoint, and ZIP transport were only
scaffolding. The revised offline target now has a lock-required terminal
generation, an exact fixed window, receipt quota marker, pre-reserved
checkpoint metadata, sealed artifact hashes, and a verifying current pointer.
However, the connected path remains deliberately disabled pending a further
independent review of root-wide streaming/extraction enforcement and current
official Binance documentation. No Binance API or download URL has been
contacted.

### Official documentation verification (2026-07-14)

Read-only web verification used the current Binance Open Platform pages:

* `https://developers.binance.com/docs/derivatives/usds-margined-futures/account/rest-api/Get-Download-Id-For-Futures-Order-History`
* `https://developers.binance.com/docs/derivatives/usds-margined-futures/account/rest-api/Get-Futures-Order-History-Download-Link-by-Id`

The pages document signed USER_DATA `GET /fapi/v1/order/asyn` with mandatory
`startTime`, `endTime`, and `timestamp`, optional `recvWindow`, request weight
1000, five requests per month shared with the front-end report page, and a
maximum documented range of one year. This pilot intentionally narrows that
maximum to one immutable, completed seven-day UTC window. The status page
documents signed `GET /fapi/v1/order/asyn/id`, mandatory `downloadId` and
`timestamp`, optional `recvWindow`, request weight 10, `processing` or
`completed`, a URL, expiration metadata, and seven-day link expiration.

The official status response example shows only a non-normative
`www.binance.com` URL placeholder; the page does not pin a download hostname,
path contract, or redirect behavior. Therefore
`OfficialDownloadHostVerified=false`: the real CLI returns
`STOP_UNVERIFIED_DOWNLOAD_HOST` before creating the private root, writing an
intent, loading credentials, or consuming async-job quota. The candidate host
is isolated from the API host allowlist and is exercised only by fake-transport
tests. No private Binance API or report download was contacted.

### One-shot recovery authority

The fixed intent is owner-only and atomically synced before the quota-bearing
GET. A surviving `INTENT_WRITTEN` without a hash-verified raw job response is
terminally ambiguous and is never retried; exactly-once request delivery cannot
be inferred across an HTTP/process crash. A successful raw response is stored
once under `private-raw/`; all checkpoints and terminal receipts contain only
its SHA-256 and an HMAC token. Resume reconstructs the raw download ID only in
memory after verifying both bindings. Status polling has fixed count, interval,
and duration bounds, and every poll advances a sealed checkpoint.

All state authority is rooted in `RootLock.OpenRootDir`; managed directories,
intent, raw response, checkpoints, generations, receipts, and current pointers
use fd-relative `openat`/`mkdirat`/`renameat` plus file and directory fsync.
Terminal current verifies its self-hash and immutable receipt generation.

## Report Center second BLOCK closure

The second review invalidated two residual assumptions. First, an expanding
outcome denylist cannot prove that an arbitrary source header is safe for a
canonical destination. Second, validating a pathname before later writes does
not bind those writes to the locked root inode.

Mapping authority is now a per-canonical audited source-header alias allowlist;
unknown aliases default to rejection. The broader denylist remains only a first
layer and includes P&L/PL, closed PL, ROI, net income, gain, loss, and yield
forms. Mapping load, header binding, and row projection independently require
the destination-specific alias. Unknown unmapped columns may appear only as
header metadata and never enter projection.

After lock acquisition, the inspector opens one root directory descriptor and
binds it to the lock's device/inode. Every subsequent inbox, mappings,
generation, staging, capacity-scan, HMAC-key, artifact, rename, and current
operation is relative to held descriptors with owner/mode/inode checks.
`current.json` publication uses a same-directory fsynced temporary file and
`renameat`; replacing the original root pathname cannot redirect a write.

Raw consistency now requires two SHA-256 passes over the same open descriptor,
with device, inode, size, mtime, and ctime unchanged. A same-inode/same-size
injected overwrite fails as `SOURCE_CHANGED` and leaves prior authority intact.
These changes do not upgrade the standing
`MANUAL_EXPORT_UNVERIFIED`/`ORIGINAL_RECEIVE_TIME_ABSENT`/
`INCOMPLETE_ATTEMPTS_DENOMINATOR` limitations.
