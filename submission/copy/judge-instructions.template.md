# Private Devpost testing instructions — fill before submission

Use this text only in Devpost's private testing field. Replace every bracketed
value. Do not put this completed text in the repository.

```text
Mithra hosted demo: https://mithrahq.com

Owner demo account
Email: [OWNER_EMAIL]
Password: [OWNER_PASSWORD]

Partner demo account
Email: [PARTNER_EMAIL]
Password: [PARTNER_PASSWORD]

Please start with Week in Review. Both accounts share the household status,
priorities, finance observation, and upcoming plans. Open Only you in each
account to see separate private records. The Owner shows a private health
comparison and a unit-correction notice. That notice does not appear in the
Partner view and does not affect shared wording.

The data is synthetic. No account can access another household. If a password
does not work, use Set or reset your password with the same allowlisted email.
```

Before posting this, reset the marked synthetic household with new password
files:

```bash
sudo mithra-installer reset-demo \
  --owner-email "[OWNER_EMAIL]" \
  --partner-email "[PARTNER_EMAIL]" \
  --owner-password-file /root/mithra-demo-owner.password \
  --partner-password-file /root/mithra-demo-partner.password
```

The email addresses must be in the installed allowlist. The password files
must be private, regular files owned by root. Do not reveal a reset URL, an API
key, a session value, or a path from the host.
