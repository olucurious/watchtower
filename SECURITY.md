# Security policy

## Reporting a vulnerability

Please report security issues privately through GitHub's private
vulnerability reporting: open the repository's **Security** tab and choose
**Report a vulnerability**. Don't open a public issue.

Include what's affected, how to reproduce it and the impact you expect. You
will get an acknowledgement as soon as possible and updates as the fix
progresses, and credit in the release notes unless you'd rather not.

## Supported versions

Fixes go into the latest release on `main`. Watchtower applies its database
migrations automatically, so upgrading is the supported way to receive them.

## Scope

Watchtower handles credentials (SDK keys, access tokens, Slack and Linear
secrets) and error data that may contain personal information. Reports about
authentication, access control, credential storage, redaction, the MCP
endpoint, injection through reported error data, and server-side requests to
unexpected hosts are especially welcome. [AGENTS.md](AGENTS.md#security-rules)
describes the rules the code is meant to keep.
