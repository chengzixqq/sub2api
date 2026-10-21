# Native mobile captcha verification

`GET /mobile/verify?nonce=<UUID>` is an unauthenticated frontend page, available even in backend-only mode. Deploy the rebuilt frontend with the existing SPA fallback. No new API or server-side authentication bypass is introduced. The native app must open the exact configured station origin over HTTPS in an ephemeral `WKWebView`.

The direct entry mounts only the challenge page, excluding the Web application shell and its authenticated startup requests. The page fetches `/api/v1/settings/public` with `credentials: omit`, uses existing captcha components/provider configuration, and mounts a provider only after the user starts verification. Disabled or incomplete provider configuration, SDK errors, expiry, cancellation, and a two-minute challenge timeout are visible states. Failed/expired challenges unmount the provider before retry. A fresh App challenge/nonce is required after a completed message.

Messages go exclusively to `window.webkit.messageHandlers.sub2Captcha.postMessage`:

```json
{"version":1,"nonce":"3a006827-701c-44e8-b153-cef42dc70234","provider":"turnstile","proof":{"turnstile_token":"<proof>"}}
{"version":1,"nonce":"3a006827-701c-44e8-b153-cef42dc70234","provider":"tencent","proof":{"tencent_captcha_ticket":"<ticket>","tencent_captcha_randstr":"<randstr>"}}
{"version":1,"nonce":"3a006827-701c-44e8-b153-cef42dc70234","provider":"aliyun","proof":{"turnstile_token":"<captchaVerifyParam>"}}
{"version":1,"nonce":"3a006827-701c-44e8-b153-cef42dc70234","cancelled":true}
```

Aliyun intentionally reuses the backend login request's `turnstile_token` field. The bridge constructs allowlisted payloads and emits at most one proof or cancellation. It rejects invalid/duplicate nonce, additional query parameters, URL userinfo/fragments, non-HTTPS or insecure context, subframes, and missing native handlers. It revalidates context before each message. Passwords, access/refresh tokens, SDK objects and settings are never forwarded or placed in URLs.

The native receiver must independently verify: main frame; exact expected scheme, host and effective port; expected UUID nonce; version 1; supported provider; provider-specific nonempty fields and no extra fields. A page-side check cannot authenticate a native receiver or protect against compromised same-origin scripts. Use an ephemeral data store, main-frame-only handler, restricted navigation and no native credentials injected into Web content. Discard proof after failed login, expiry or cancellation; do not treat challenge completion as successful login. Do not log proofs.

## Discovery and provider declaration (backend)

`GET /api/v1/settings/public` also returns:

- `captcha_provider`: the provider the backend verifies against, using the same three enabled switches as login: `""` (none), `turnstile`, `tencent`, `aliyun`, or `conflict` (several enabled; captcha-protected requests return 503 `CAPTCHA_PROVIDER_CONFLICT`). An older backend omits the key.
- `mobile_verify_path`: `/mobile/verify` when the frontend embedded in this binary contains the bridge page, otherwise `""`. A binary built without the embedded frontend always reports `""`, even when a separately deployed frontend serves the page.

`POST /api/v1/auth/login`, `/auth/register`, `/auth/send-verify-code`, `/auth/forgot-password` and `/auth/passkey/login/begin` accept two optional fields:

- `captcha_provider`: the `provider` from the bridge envelope. When it differs from the provider in force the request fails with 400 `CAPTCHA_PROVIDER_MISMATCH` before any captcha verification; `metadata.captcha_provider` carries the current provider. Run a fresh challenge and retry. Omitting it skips the check.
- `aliyun_captcha_param`: the Aliyun `captchaVerifyParam`. Native apps may map the Aliyun envelope's `proof.turnstile_token` to this field. When it is empty or blank the backend falls back to `turnstile_token`, which Web keeps using.

The bridge page itself is unchanged and still emits `turnstile_token` for Aliyun.

Validation from `frontend/`:

```sh
pnpm exec vitest run src/utils/__tests__/mobileCaptchaBridge.spec.ts src/views/public/__tests__/MobileVerifyView.spec.ts src/__tests__/mobileVerificationBootstrap.spec.ts src/router/__tests__/mobile-verification-route.spec.ts
pnpm run typecheck
pnpm exec eslint src/main.ts src/router/index.ts src/utils/mobileCaptchaBridge.ts src/utils/__tests__/mobileCaptchaBridge.spec.ts src/views/public/MobileVerifyView.vue src/views/public/__tests__/MobileVerifyView.spec.ts src/__tests__/mobileVerificationBootstrap.spec.ts src/router/__tests__/mobile-verification-route.spec.ts
```
