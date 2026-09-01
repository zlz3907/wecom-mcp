# Enterprise WeCom application messages

Send an application message only when the current user explicitly authorizes that notification and identifies one recipient. Resolve the recipient to one enabled Enterprise WeCom `userid`; never use a display name as the final send target.

## Text

Use `wecom_send_app_message` with the verified `recipient_userid`, UTF-8 text, a stable idempotency key, and the active identity binding. Do not include credentials, binding handles, verification codes, or sensitive record payloads in the text.

## Image or ordinary file

Use `wecom_send_app_media_message` only for a file already available in the current trusted WorkBuddy context or workspace.

1. Read the exact file bytes without modifying them.
2. Use `media_type=image` only for a filename and content that are both JPG/JPEG or PNG. Use `media_type=file` for an ordinary file.
3. Base64-encode the bytes using standard padded Base64 and compute the lowercase SHA-256 of the original bytes.
4. Call the tool with one verified `recipient_userid`, the original safe basename, Base64, SHA-256, stable idempotency key, and active identity binding.
5. Treat only `state=sent` with a verified message receipt as sent. Report `sent_idempotency_completion_pending` exactly and do not replay an uncertain upload or send.

Images are limited to 10 MiB and ordinary files to 20 MiB. Do not download a caller-provided URL, guess a local path, send a directory, or copy attachment bytes into Zoop records, logs, or chat output.

## Prohibited targets

Never use `@all`, departments, tags, external contacts, group chats, or multiple user IDs. The connector credential and self-built application are delivery identities, not message recipients or Zoop business actors.
