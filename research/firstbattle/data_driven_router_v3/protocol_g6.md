# Data-Driven Router V3 g6 Account-State Transition Overlay

> Document hash: `e14ca0b8f36c1b5aca68798a22b48d937ef9086bb2d1bdee709f9c7771248328`
> Overlay hash: `08b609e3271029b1e9f772921e325ef7c7fd30326589ae9c03cc86d73d9c844b`
> Market dataset hash: `0c084dc440761569eeb0bbf9b078d653daf2688d65f5a1ac7c2c977c26691fa1`
> Generation: `firstbattle-data-driven-router-v3-g6`
> Status: `PREREGISTERED_REWARD_LABELS_STILL_SEALED_PENDING_INDEPENDENT_REVIEW`

The 4,025,979-row market StateRow dataset remains unchanged and approved. g6 adds a separate, neutral-runner-owned account state layer before any model reward is unsealed.

## Account context

The neutral runner emits current position direction/fraction, fixed notional, weighted entry, age, DecisionTime unrealized return, cumulative signed funding, cash, equity, retained base, withdrawals, leverage, free equity, open positions and pending actions. DecisionTime unrealized return is current account context; future or terminal PnL remains forbidden.

## Model output

Models do not emit verbs. They emit:

- target signed position fraction in `[-1,-.5,-.25,-.1,0,.1,.25,.5,1]`;
- target horizon `1/4/24h`;
- delay `0/1/2/3` bars.

The neutral state machine derives:

```text
flat -> 0        = NO_TRADE
flat -> positive = LONG
flat -> negative = SHORT
same sign/equal  = HOLD
same sign/larger = ADD
same sign/smaller= TRIM
nonzero -> 0     = CLOSE
opposite sign    = REVERSE
```

REVERSE is exactly CLOSE with full exit cost, account recheck, then conditional opposite open with full entry cost.

## Accounting

- ADD updates fixed notional and weighted average entry.
- TRIM realizes proportional PnL/funding and preserves the remainder's entry.
- HOLD keeps entry/notional and accrues MTM/funding.
- CLOSE realizes all and removes the position.
- Pending actions execute only after delay; MTM/funding continue before maturity.
- Unmatured pending is discarded at EOF.
- Withdrawal occurs after MTM/funding/exits and before new risk.

## Training and certification

Discovery may use deterministic canonical hypothetical contexts (FLAT/LONG/SHORT, age 1/4/24, size .1/.25/.5/1) generated and sampled by neutral hash priority. Certification never uses these hypothetical states; it uses real chronological AccountContext from the one shared runner.

All four models and baselines consume the same runner, dataset and transition hash. Model-owner-specific account bookkeeping is forbidden.

## Safety

```text
market_state_layer=APPROVED
account_state_layer=DEFINED_NOT_IMPLEMENTED
reward_labels=SEALED_NOT_READ_OR_CREATED
routes_started=0
NO_GO
```

## Hash

JSON: set `hashes.overlay_hash` to `null`, then run repository `datatruth.HashJSON`.

Markdown: replace the single `Document hash` value with `SELF_EXCLUDED`, then SHA-256 the UTF-8 LF bytes.
