---
name: webvuln
description: Web vulnerability hunting playbook. Use after recon, when you have specific hosts/endpoints to test for IDOR/BAC, injection, auth flaws, SSRF, and known CVEs. Emphasizes real PoC + concrete impact.
allowed-tools:
  - shell
  - http
  - web_search
  - web_fetch
  - file_write
---

# Web vuln hunting playbook

You are testing specific endpoints the user has handed you (or that came out of the `recon` skill). Every finding must come with a real PoC and a concrete impact statement — no theoretical bugs.

Default to curl and the built-in `http` tool. Do not pull in heavy scanners (nuclei, sqlmap, ffuf, etc.) unless the user explicitly asks for them or you have manually confirmed a bug and need a scanner only to characterize the bug class.

Execution rule: substitute real target values before running commands. Never write literal placeholders such as `<TARGET>`, `<vulnerable-path>`, or `<PoC body>` to files. If a value is unknown, ask once or derive it from `/target`.

## 1. Triage the target
Fetch the landing page with the `http` tool or curl. Note:
- Framework / language signals (cookies, headers, error pages)
- Authentication scheme (cookie, Bearer, basic)
- API style (REST / GraphQL / gRPC)
- Anything that suggests a known CVE family (versioned banner, vendor product name)

If you spot a versioned product, immediately `web_search "<product> <version> CVE"` and `web_fetch` the top advisory. Reproduce the CVE manually with curl before reporting.

## 2. Known-CVE pass (manual, curl-driven)
For each suspected CVE pulled from the advisory, craft the curl that proves it — single request when possible:

```
TARGET="https://app.example.com" # replace with the scoped target before running
curl -ksS -X POST "$TARGET/vulnerable-path" \
  -H 'Content-Type: application/json' \
  -d '{"replace":"with-real-poc-body"}' \
  -w "\nHTTP %{http_code}  size=%{size_download}  time=%{time_total}\n"
```

If the advisory describes a recognizable pattern (template injection, deserialization, etc.) and the user has explicitly authorized broader scanning, then — and only then — reach for `nuclei` against the single host. Otherwise stay manual.

## 3. Auth + access control (IDOR / BAC)
- Identify any numeric or UUID identifiers in the URL path or query (`/api/users/12345`, `/orders/?id=...`).
- With user-provided session A, fetch a resource you own.
- Swap the identifier to another user's value (or use a second session from the user) and replay with curl or the `http` tool.
- A 200 with foreign data = IDOR. Capture the curl one-liner and the response excerpt into `findings/idor-<endpoint>.txt`.

Example IDOR sweep with two sessions:

```
TARGET="https://app.example.com" # replace with the scoped target before running
for id in $(seq 1 50); do
  body=$(curl -ksS -H "Cookie: $SESSION_B" "$TARGET/api/users/$id" | jq -r '.email // empty')
  [ -n "$body" ] && echo "$id $body"
done
```

## 4. Injection surfaces (curl-first)
For each parameter (query, body, header, cookie):
- Inject simple probes (`'`, `"`, `<x>`, `${7*7}`, `{{7*7}}`) with curl or the `http` tool.
- 500s / reflected payloads / arithmetic evaluation → escalate to a targeted PoC.

Quick reflected-XSS probe with curl:

```
TARGET="https://app.example.com" # replace with the scoped target before running
for p in q s search query keyword; do
  curl -ksS "$TARGET/?$p=pf$(date +%s)<svg/onload=alert(1)>" \
    | grep -o "pf[0-9]*<svg.*alert(1)>" || true
done
```

For SQLi: only after curl-level manual confirmation (timing differences, error strings) and only with the user's explicit OK, escalate to `sqlmap` with `--batch --level=2 --risk=1 --random-agent` against the single endpoint. Default path is manual `' OR sleep(5) -- ` style probes via curl, then exfil via UNION when you know the schema.

## 5. SSRF & open redirects
Any parameter that takes a URL or hostname: try `http://127.0.0.1`, `http://169.254.169.254/latest/meta-data/`, and an out-of-band canary the user provides. Compare response timings and bodies with curl `-w "%{time_total} %{size_download}\n"`.

## 6. Report
For each confirmed finding, write `findings/<id>-<short-title>.md` with:
- Title, severity (VRT-aligned), category
- Affected endpoint(s)
- Step-by-step reproduction as the **exact curl one-liner** the reviewer can copy (with placeholders for session tokens)
- Observed response excerpt proving the bug
- Concrete impact (what data is exposed, what state can be changed, what privilege is escalated)
- Suggested remediation

Stop after writing the report. Do not chain into further exploitation unless the user explicitly asks.
