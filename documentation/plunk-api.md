# Plunk transactional email contract

Research date: 2026-07-19. This note covers only the minimal hosted-Plunk
contract Mithra needs for password links, invitations, and generic nudges.

## Required request

- Send `POST https://next-api.useplunk.com/v1/send` with
  `Authorization: Bearer <sk_* secret key>`, `Content-Type: application/json`,
  and optionally `Accept: application/json`. The secret key identifies its
  Plunk project; a public `pk_*` key cannot send email.
- For Mithra's inline messages, send only:

  ```json
  {
    "to": "recipient@example.com",
    "subject": "Your Mithra password link",
    "body": "<p>Escaped HTML content</p>",
    "from": {
      "name": "Mithra",
      "email": "sender@mithrahq.com"
    }
  }
  ```

  `to` is required. Without a template, non-empty `subject` and HTML `body`
  are required. A subject cannot contain newlines and is limited to 998
  characters. `from` is required unless a template supplies it; the
  `{name,email}` form is preferred because the separate top-level `name` field
  is deprecated. Mithra does not need templates, contact data, attachments,
  subscription changes, custom headers, or an SDK.

Sources: [API overview](https://docs.useplunk.com/api-reference/overview),
[send transactional email](https://docs.useplunk.com/api-reference/public-api/sendEmail),
[API keys](https://docs.useplunk.com/guides/api-keys).

## Response and errors

- Success is HTTP `200` with a public-API envelope shaped as
  `{"success":true,"data":{"emails":[...],"timestamp":"..."}}`. The client
  should require both status `200` and `success: true`; missing, malformed, or
  false `success` is delivery failure.
- Canonical errors use
  `{"success":false,"error":{"code":"...","message":"...","statusCode":N,"requestId":"..."},"timestamp":"..."}`.
  Validation errors may add `errors`; other errors may add `details` and
  `suggestion`. Relevant documented statuses are `400`, `401`, `402`, `403`,
  `404`, `409`, `422`, `429`, and `500`.
- Preserve Mithra's existing privacy boundary: return its generic delivery
  error and never expose or log provider response bodies, recipient addresses,
  reset URLs, or credentials. A bounded `requestId` may be retained only if the
  structured logging policy explicitly permits it.

Sources: [send response](https://docs.useplunk.com/api-reference/public-api/sendEmail),
[response format and statuses](https://docs.useplunk.com/api-reference/overview),
[error reference](https://docs.useplunk.com/api-reference/errors).

## Timeout, retry, and idempotency

- Plunk documents no client request timeout. Keep Mithra's existing 10-second
  `http.Client` timeout.
- Plunk documents `429` rate limiting and recommends exponential backoff for
  transient errors, but a blind retry after a lost response could duplicate a
  password link or nudge. The minimal safe replacement therefore performs no
  automatic retry.
- If Mithra later adds retry, send the same stable `Idempotency-Key` on every
  attempt and retry only transport errors, `429`, or `5xx`, with a small capped
  backoff. Keys are project-scoped, 1-255 printable ASCII characters, retained
  for 24 hours, and reuse is refused with `409` without another send.

Sources: [rate limits](https://docs.useplunk.com/api-reference/overview),
[retry guidance](https://docs.useplunk.com/api-reference/errors),
[idempotency header](https://docs.useplunk.com/api-reference/public-api/sendEmail).

## Sender and domain prerequisites

- Add `mithrahq.com` to the Plunk project's Domains settings before using a
  `from` address on that domain. Plunk requires the sender domain to be
  verified.
- Publish the exact DNS values Plunk supplies: three DKIM CNAME records, one
  SPF TXT record, and one bounce-handling MX record. If `mithrahq.com` already
  has SPF, merge Plunk's mechanism into that single SPF record; two SPF records
  both fail.
- DMARC and a custom MAIL FROM subdomain are optional but recommended. DNS
  verification can take up to 72 hours, so confirm every required record as
  verified in Plunk before the live password-reset test.
- Keep the `sk_*` key server-side in the existing systemd-credential model;
  never commit it. Plunk key rotation invalidates the old public and secret key
  pair immediately, with no grace period.

Sources: [domain verification](https://docs.useplunk.com/guides/verifying-domains),
[sender requirement](https://docs.useplunk.com/api-reference/public-api/sendEmail),
[key storage and rotation](https://docs.useplunk.com/guides/api-keys).

## Minimal Go mapping

Use only `net/http`, `encoding/json`, and `html` from the standard library.
Map Mithra's `Message.Text` to escaped HTML before serialization; never insert
message text or URLs into HTML unescaped. No new production dependency is
needed.
