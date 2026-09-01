---
name: takeover
description: Subdomain takeover playbook — sweep subdomains for dangling CNAMEs / NS records pointing at unclaimed third-party resources (GitHub Pages, S3, Heroku, Azure, Netlify, Shopify, ...), confirm with the engine's HTTP fingerprint, then prove impact by claiming the resource in scope. Use when enumerating subdomains for dangling CNAME/NS records pointing at unclaimed third-party services.
allowed-tools:
  - http
  - shell
  - read_payloads
  - file_write
---

# Subdomain takeover playbook

A subdomain points (via CNAME, NS, or an A record on a shared host) at a
third-party service. The resource on that service was deleted, expired,
or never claimed — but the DNS record still exists. An attacker registers
the same resource on the provider and serves arbitrary content on the
victim's hostname. Severity is usually **high** to **critical** because
the takeover puts the attacker inside the victim's origin (cookie scope,
CSP allowlists, OAuth `redirect_uri` allowlists, SAML SP entity IDs,
email DKIM/SPF includes, ...).

> Stay in scope. Only test takeovers against domains the program
> explicitly authorizes. A successful takeover *is* serving content on
> someone else's host — drop a benign HTML file (`takeover proof for
> <handle>, contact <email>`) and stop.

Execution rule: operate on real subdomains and provider fingerprints from the scoped program. Never write literal placeholders such as `<provider>`, `<handle>`, or `<email>` to files; ask once for proof text if a provider requires a claim page.

## 1. Enumerate every subdomain

Use whatever recon you have. Curl-first sources you can hit without
extra tooling:

```sh
# CT logs via crt.sh
curl -s 'https://crt.sh/?q=%25.target.example.com&output=json' \
  | jq -r '.[].name_value' | sed 's/^\*\.//' | sort -u > subs.txt

# Anubis-DB
curl -s 'https://jldc.me/anubis/subdomains/target.example.com' \
  | jq -r '.[]' >> subs.txt

# Hackertarget (rate-limited)
curl -s 'https://api.hackertarget.com/hostsearch/?q=target.example.com' \
  | cut -d, -f1 >> subs.txt

sort -u subs.txt -o subs.txt
```

If [[recon]] is loaded, prefer those flows for the enumeration step.

## 2. Resolve each name — CNAME first, then A/AAAA

```sh
# Pull CNAMEs in one batch (works on macOS/Linux with dig)
while read -r sub; do
  cname=$(dig +short CNAME "$sub" | head -n1)
  if [ -n "$cname" ]; then printf '%s\tCNAME\t%s\n' "$sub" "$cname"; fi
done < subs.txt > resolved.tsv

# NS delegation (rare but high-severity)
while read -r sub; do
  ns=$(dig +short NS "$sub")
  if [ -n "$ns" ]; then printf '%s\tNS\t%s\n' "$sub" "$(echo "$ns" | tr '\n' ',')"; fi
done < subs.txt >> resolved.tsv
```

You're looking for:

- **CNAME → third-party provider** (e.g. `app.target.com → app.elasticbeanstalk.com`) where the resource at that provider is unclaimed.
- **NS → vanished zone** (`zone.target.com NS ns1.dnsmadeeasy.com` where the
  zone no longer exists on dnsmadeeasy).
- **CNAME → NXDOMAIN** — the cleanest signal: the CNAME target itself
  doesn't resolve. Always investigate.

```sh
# Find CNAMEs whose target doesn't resolve at all
awk -F'\t' '$2=="CNAME" {print $3}' resolved.tsv | while read -r t; do
  dig +short "$t" >/dev/null 2>&1 || echo "$t (NXDOMAIN target)"
done
```

## 3. Match HTTP fingerprints

Pull the full fingerprint database:

```
read_payloads(skill="takeover", file="fingerprints.json")
```

The JSON Lines file lists, for each provider:

- `service` — human label.
- `cname` — regex of the target hostname.
- `status` — `vulnerable` / `edge-case` / `not-vulnerable`.
- `fingerprint` — string to grep for in the HTTPS response body.
- `http_status` — typical status code (404, 200, etc.).
- `notes` — claim-flow gotchas.

For each CNAME match, fetch the response and grep:

```sh
# Example: subdomain points at GitHub Pages
curl -sk -H "Host: lost.target.com" https://lost.target.com/ \
  | grep -F "There isn't a GitHub Pages site here."
```

A hit on `status: vulnerable` plus the fingerprint in the response body is
the takeover signal. Don't trust the fingerprint alone — many providers
serve the same string on a legitimately-not-yet-deployed site that's still
claimed by the owner. **Confirm-step (4) is mandatory.**

## 4. Confirm before reporting

The trap: the resource may be unclaimed *by anyone visible to you*, but
the legitimate owner may still hold it via an account you can't see (e.g.
the Heroku app exists in their account but is paused). False-positive
report rates on subdomain takeover are notorious.

Confirmation paths, in order of preference:

1. **Out-of-band canary.** Attempt to register the resource (e.g. create
   a Heroku app named `lost-target`). If the provider says "name taken"
   without serving you the fingerprint, it's a false positive — someone
   has it.
2. **Successful claim + benign content.** When the registration succeeds,
   serve a static HTML file proving ownership:

   ```html
   <!doctype html>
   <title>Subdomain Takeover PoC</title>
   <h1>Subdomain Takeover</h1>
   <p>This subdomain (<code>lost.target.com</code>) was claimable on
      <em>&lt;provider&gt;</em> by anyone. Reported by &lt;handle&gt;
      to &lt;program&gt;. Contact: &lt;email&gt;.</p>
   ```

   Fetch the subdomain again over HTTPS — your content must come back.
3. **Screenshot + curl response** in the report.

Do **NOT**:
- Serve real-looking content (login forms, fake updates).
- Run any script in the page.
- Hold the resource beyond what's needed for the PoC; release it after
  the report is triaged.
- Use takeovers to phish, even for "research."

## 5. NS takeover (variant)

If a subdomain delegates DNS to a third-party provider via NS records and
the zone is unclaimed on that provider, you control all records under
the subdomain — far more dangerous than a single CNAME takeover. Common
providers seen here: DNSMadeEasy, Bizland, EasyDNS, Yahoo Small Business,
NS1, Hurricane Electric, MyDomain, Domain.com.

Detection: `dig NS sub.target.com` returns a provider's nameservers, but
querying those nameservers for the zone returns `REFUSED` or `SERVFAIL`.

```sh
for ns in $(dig +short NS sub.target.com); do
  echo "=== $ns ==="
  dig @"$ns" sub.target.com SOA
done
```

A `REFUSED`/`SERVFAIL` from *all* of the listed nameservers, combined
with the provider being one that lets you create zones with arbitrary
names, is the takeover primitive.

## 6. Key engines (quick reference — full DB in payloads)

| Service | CNAME pattern | Fingerprint substring | Status |
|---|---|---|---|
| GitHub Pages | `*.github.io` | `There isn't a GitHub Pages site here.` | vulnerable |
| Heroku | `*.herokuapp.com`, `*.herokudns.com` | `No such app` / `There's nothing here, yet.` | vulnerable |
| AWS S3 (website) | `*.s3-website-*.amazonaws.com`, `*.s3.amazonaws.com` | `NoSuchBucket` | vulnerable |
| Azure App Service | `*.azurewebsites.net` | `404 Web Site not found.` | vulnerable |
| Azure Cloud Service | `*.cloudapp.net`, `*.cloudapp.azure.com` | `404 Web Site not found.` | vulnerable |
| Azure Traffic Manager | `*.trafficmanager.net` | NXDOMAIN on profile | vulnerable |
| Azure Storage | `*.blob.core.windows.net` | NXDOMAIN | vulnerable |
| Azure CDN | `*.azureedge.net` | NXDOMAIN / Bad Request | edge-case |
| Netlify | `*.netlify.app`, `*.netlify.com` | `Not Found - Request ID` | vulnerable |
| Heroku SSL endpoint | `*.herokussl.com` | varies | edge-case |
| Vercel / Now | `*.vercel.app`, `cname.vercel-dns.com` | `404: NOT_FOUND` / `The deployment could not be found` | edge-case |
| Shopify | `*.myshopify.com` | `Sorry, this shop is currently unavailable.` | vulnerable |
| Tumblr | `*.tumblr.com` (custom domain) | `Whatever you were looking for doesn't currently exist at this address.` | vulnerable |
| Surge.sh | `*.surge.sh` | `project not found` | vulnerable |
| Ghost | `*.ghost.io` | `The thing you were looking for is no longer here, or never was` | vulnerable |
| Pantheon | `*.pantheonsite.io` | `The gods are wise, but do not know of the site which you seek.` | vulnerable |
| Squarespace | various | `No Such Account` / `Squarespace - No Such Account` | edge-case |
| Tilda | `*.tilda.ws` | `Please renew your subscription` | vulnerable |
| Unbounce | `*.unbouncepages.com` | `The requested URL was not found on this server.` | vulnerable |
| UserVoice | `*.uservoice.com` | `This UserVoice subdomain is currently available!` | vulnerable |
| Strikingly | `*.s.strikinglydns.com` | `PAGE NOT FOUND.` | vulnerable |
| Helpjuice | `*.helpjuice.com` | `We could not find what you're looking for.` | vulnerable |
| HelpScout | `*.helpscoutdocs.com` | `No settings were found for this company:` | vulnerable |
| Bitbucket | `*.bitbucket.io` | `Repository not found` | vulnerable |
| Cargo Collective | `*.cargocollective.com` | `If you're moving your domain away from Cargo` | vulnerable |
| Statuspage | `*.statuspage.io` | `You are being redirected.` (302 to statuspage 404) | edge-case |
| Acquia | `*.acquia-sites.com` | `The site you are looking for could not be found.` | vulnerable |
| Aha | `*.aha.io` | `There is no portal here ... sending you back to Aha!` | vulnerable |
| Anima | `*.animaapp.io` | `If this is your website and you've just created it` | vulnerable |
| Brightcove | various | `<p class="bc-gallery-error-code">Error Code: 404</p>` | vulnerable |
| Campaign Monitor | `createsend.com` | `Trying to access your account?` | vulnerable |
| Canny | `*.canny.io` | `Company Not Found` | vulnerable |
| Fastly | `*.fastly.net` | `Fastly error: unknown domain` | edge-case |
| Frontify | `*.frontify.com` | `Brand not found` | vulnerable |
| Gemfury | `*.fury.site` | `404: This page could not be found.` | vulnerable |
| GetResponse | varies | `With GetResponse Landing Pages, lead generation has never been easier` | vulnerable |
| Hatena Blog | `hatenablog.com` | Japanese error string | vulnerable |
| HelpRescue | `*.helprace.com` | `Help Center Closed` | edge-case |
| JetBrains | `*.youtrack.cloud` | `is not a registered InCloud YouTrack` | vulnerable |
| Kinsta | `*.kinsta.cloud` | `No Site For Domain` | edge-case |
| LaunchRock | `*.launchrock.com` | `It looks like you may have taken a wrong turn somewhere.` | vulnerable |
| Mashery | `*.mashery.com` | `Unrecognized domain` | edge-case |
| Pingdom | `*.stats.pingdom.com` | `pingdom` page | edge-case |
| Proposify | `*.proposify.com` | `If you need immediate assistance` | vulnerable |
| Readme | `*.readme.io` | `Project doesnt exist... yet!` | vulnerable |
| SendGrid | varies | parked landing | edge-case |
| ShortIO | `*.shortio.app` | `404, please check the URL.` | vulnerable |
| Smartling | `*.smartling.com` | `Domain is not configured` | vulnerable |
| Thinkific | `*.thinkific.com` | `You may have mistyped the address or the page may have moved.` | vulnerable |
| Uberflip | `*.uberflip.com` | `The page you are looking for is not found` | vulnerable |
| Vend | `*.vendecommerce.com` | `Looks like you've traveled too far into cyberspace.` | vulnerable |
| Webflow | `proxy-ssl.webflow.com` | `The page you are looking for doesn't exist or has been moved.` | vulnerable |
| Wishpond | varies | `https://www.wishpond.com/404?campaign=` | vulnerable |
| Wordpress | `*.wordpress.com` | `Do you want to register` | edge-case |
| WP Engine | `*.wpengine.com` | `The site you were looking for couldn't be found.` | edge-case |
| Worksites | varies | `Hello! Sorry, but the website you’re looking for doesn’t exist.` | vulnerable |
| Cloudfront | `*.cloudfront.net` | `The request could not be satisfied` | edge-case |
| Google Cloud Storage | `*.storage.googleapis.com` | `NoSuchBucket` | vulnerable |

The full database — including HTTP status codes, claim notes, and edge-case
explanations for every entry — is in `payloads/fingerprints.json` (see the
`read_payloads` invocation in section 3).

## 7. Tooling shortcuts (when available)

If you have these installed, they automate sections 1–3:

- `subjack` — Go scanner with built-in fingerprints. `subjack -w subs.txt -t 100 -timeout 30 -ssl -c fingerprints.json -v`
- `subzy` — newer alternative.
- `nuclei` — `nuclei -l subs.txt -tags takeover` runs the community templates.
- `tko-subs` — also script-based.
- `aquatone` — screenshots + detection in one pass.

You do **not** need any of these to do the work; sections 2–4 are
do-able with `dig` + `curl` + the JSON fingerprint file.

## 8. Reporting

For each confirmed takeover:

- Vulnerable subdomain (`lost.target.com`).
- DNS resolution path (`lost.target.com CNAME bucket.s3.amazonaws.com → unclaimed`).
- The exact provider + service.
- HTTP response showing the fingerprint (curl one-liner + body excerpt).
- PoC: your claimed resource serving a benign proof file.
- Impact paragraph specific to the engagement: which cookies, CSP rules,
  OAuth `redirect_uri` lists, SAML entity IDs, mail SPF/DKIM, or session
  domains include the parent — that's where the severity comes from.
- Remediation: remove the dangling DNS record OR re-claim the resource.
- Suggested severity: critical when the parent's auth cookies / SSO /
  payment flows reach the subdomain; high otherwise.

Release the claimed resource after the program triages.
