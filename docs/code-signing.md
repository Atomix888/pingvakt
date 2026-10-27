# Code signing

Unsigned builds work, but Windows shows a SmartScreen warning ("Windows protected your PC") and macOS refuses to open the app until the user right-clicks → Open. The build in GitHub Actions signs automatically as soon as the secrets below exist. Without them it builds unsigned files as before.

Add secrets under **GitHub → the repository → Settings → Secrets and variables → Actions → New repository secret**.

## Windows

Since 2023 code-signing keys must be kept in hardware or a cloud service, so a plain `.pfx` file is rarely possible any more. Two alternatives are supported:

### A) Azure Trusted Signing (recommended, about 10 USD/month)

1. In the Azure portal, create a **Trusted Signing account** (region e.g. West Europe) and complete **identity validation** for your company (organisation number, takes a few days).
2. Create a **certificate profile** of type *Public Trust*.
3. Create an **app registration** in Entra ID with a client secret, and give it the role **Trusted Signing Certificate Profile Signer** on the account.
4. Add these secrets:

| Secret | Example |
|---|---|
| `AZURE_TENANT_ID` | the directory ID |
| `AZURE_CLIENT_ID` | the app registration's application ID |
| `AZURE_CLIENT_SECRET` | the client secret |
| `AZURE_SIGN_ENDPOINT` | `weu.codesigning.azure.net` (depends on region) |
| `AZURE_SIGN_ACCOUNT` | the Trusted Signing account name |
| `AZURE_SIGN_PROFILE` | the certificate profile name |

### B) Certificate file

If your certificate provider gives you an exportable `.pfx` (or a cloud key that jsign supports), add:

| Secret | Content |
|---|---|
| `WIN_CERT_PFX` | the file as base64: `base64 -w0 cert.pfx` |
| `WIN_CERT_PASSWORD` | the password |

Signed files: `PingVakt.exe`, the installer, the Windows agents and the Windows server. Signing is done with [jsign](https://ebourg.github.io/jsign/) and timestamped, so the signatures stay valid after the certificate expires.

Note: the Docker image builds its own agents and does not sign them. Agents installed with the PowerShell command are not affected by SmartScreen.

## macOS

1. Join the **Apple Developer Program** (99 USD/year) as an organisation (needs a D-U-N-S number) or as an individual.
2. In Xcode or at developer.apple.com, create a **Developer ID Application** certificate. Export it from Keychain Access as `.p12` with a password.
3. Create an **app-specific password** at appleid.apple.com (Sign-In and Security → App-Specific Passwords).
4. Add these secrets:

| Secret | Content |
|---|---|
| `APPLE_CERT_P12` | `base64 -i cert.p12` |
| `APPLE_CERT_PASSWORD` | the .p12 password |
| `APPLE_ID` | your Apple ID email |
| `APPLE_TEAM_ID` | the 10-character team ID (Membership details) |
| `APPLE_APP_PASSWORD` | the app-specific password |

The app is then signed with hardened runtime, the `.dmg` is signed and notarized by Apple, and the notarization ticket is stapled so it opens without warnings even offline.

## Checking

- Windows: right-click the file → Properties → Digital Signatures, or `signtool verify /pa PingVakt.exe`.
- macOS: `spctl -a -vv /Applications/PingVakt.app` should say `accepted, source=Notarized Developer ID`.
