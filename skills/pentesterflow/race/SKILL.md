---
name: race
description: Race condition / TOCTOU playbook — limit overrun (one-time codes used twice, gift cards spent twice), single-packet attack (last-byte sync) to force parallel processing, and state-confusion races (file upload + read, order before payment). Use when timing-sensitive logic could be abused — one-time codes, coupons/gift cards, balance or limit checks, double-spend.
allowed-tools:
  - http
  - shell
  - file_write
---

# Race / TOCTOU playbook

Confirm the target is in scope for stress-style testing (multiple parallel requests can look like abuse). Then identify the candidate operation.

Execution rule: capture a real request for the operation and replay that request with concrete cookies/body values. Never write literal placeholders such as `<sess>` to files; ask once if a session or replayable request is missing.

## Targets worth racing

- Redeem a gift card / promo code (does the second redeem succeed?)
- Cast a vote / claim a one-per-user reward
- Withdraw / transfer balance (does it deduct twice?)
- Submit a 2FA code (can N parallel guesses each be checked against an unused-attempts counter?)
- Apply a discount that's supposed to be once-per-cart
- Send a friend / invite request (creates duplicate rows / privileges)
- Confirm an email (mark verified twice with different addresses)
- Upload then read a file (write `safe.png`, then race a swap to `evil.svg` before AV scans)

## 1. Burp-style "single packet attack" (last-byte sync) — preferred

Modern servers buffer HTTP/2 frames. The classic trick: send N requests where each is fully serialized *except* the last byte. Then in a single TCP write, flush the final byte of all N. The server receives them effectively simultaneously and dispatches them in parallel — bypassing nearly all software-side rate limiting.

You can do this with a small Go/Python script. Skeleton (Python, HTTP/2):

```python
import asyncio, ssl
import httpx

async def main():
    transport = httpx.AsyncHTTPTransport(http2=True)
    async with httpx.AsyncClient(transport=transport, verify=False) as c:
        # Build N partial requests, then race the last byte.
        # In practice: use turbo-intruder or a Go h2 client for byte-level control.
        ...
asyncio.run(main())
```

For the highest-fidelity version, use Burp Suite's **Turbo Intruder** (`race-single-packet-attack.py` template) — it's the de facto tool. The `shell` tool can run it if installed.

## 2. Fallback: HTTP/1.1 pipelined burst

If HTTP/2 isn't available, fire N requests over a single keep-alive connection back-to-back without waiting for responses:

```sh
# 50 parallel uses of the same coupon code
seq 50 | xargs -P 50 -I{} curl -s -X POST https://target/redeem \
  -H 'Cookie: session=<sess>' \
  -d 'code=GIFT100'
```

Less reliable than single-packet but often enough.

## 3. Confirm a race actually happened

For each candidate operation, measure the *invariant* twice:

- Before: query balance / count / state.
- Fire N parallel requests.
- After: query balance / count / state.

If invariant differs by more than 1× the per-request delta, you have a race. Example: gift card worth $50, used 5× before lockout → $250 credit. Confirmed.

## 4. TOCTOU on file/auth flows

Time-Of-Check / Time-Of-Use bugs:

- Upload + scan + serve: race the swap between AV scan and final write.
- "Is admin?" check on session, then long-running task that uses the role: race a logout/role-change between the check and the use.
- Symlink race on file-write tools that write through user-supplied paths.

## 5. Mitigation patterns (so you know what defeats you)

If you see any of these, the race probably isn't winnable — refocus:

- `SELECT ... FOR UPDATE` / explicit row lock.
- Optimistic concurrency: every update carries a `version` field that's CAS'd.
- Idempotency keys on the request: server stores the key+result and replays.
- `UNIQUE (user_id, code_id)` constraint with `ON CONFLICT DO NOTHING`.

## Reporting

Required for a credible finding:
- The exact race window measured (`N=50`, `mean=20ms apart`).
- Before / after invariant query results.
- Concrete impact: "$X credit overspent" / "Y duplicate privileged rows created" / "Z brute-force guesses processed before lockout".

A race with no measurable impact (e.g., creates a second pending order that the next cron job dedupes) is **info** at best.
