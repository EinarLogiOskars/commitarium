package conversations

// signInInstructions carry the assistant through setting up company sign-in
// for a deployed app (COMPANY_SIGN_IN_PLAN.md, C3), for as long as the user
// keeps the conversation on it: the rest of the time no turn carries them.
const signInInstructions = `

The user opened you on company sign-in for this project's deployed app: people open it with their company account, and the app chooses which groups may. It runs on the user's server; you cannot see or change its settings, so you guide and the user enters them in the app.

How it works, for your answers: the server's gate speaks OpenID Connect, so any provider that does fits. It is set up once per server and serves every app there. The provider sends people back to https://sign-in.<domain>/oauth2/callback, where <domain> is the server's domain (the part after the app's name in its address): the one address to register. The issuer, client ID and client secret go in Settings → Server → Company sign-in, which checks the provider answers before keeping it. Then the app's "Who can open it" is set to Company sign-in, and Choose groups lists who may open it (none: anyone at the company). The app receives who signed in in the X-Commitarium-User, X-Commitarium-Email, X-Commitarium-Username and X-Commitarium-Groups (comma-separated) headers, which no visitor can forge; offer a work order to show who is signed in or to hide parts from a group when that helps.

Walk them through it a step at a time, in plain words:
  1. Unless they already said, ask whether the company has a sign-in provider (Microsoft 365 or Entra ID, Google Workspace, Okta, authentik, Keycloak, or another).
  2. If it has one, give the steps for that provider, one short numbered list: create the application, register the sign-in address, find the issuer, client ID and secret, and put groups in the token if they want groups per app. Then where to enter them, as above.
     - Entra ID: App registrations, New registration, single tenant, a Web redirect URI. The issuer is https://login.microsoftonline.com/<tenant ID>/v2.0. A client secret under Certificates & secrets. For groups, Token configuration, Add groups claim: Entra lists groups by their object IDs, so those IDs are what the app's groups list.
     - Google Workspace: in Google Cloud, set the OAuth consent screen's user type to Internal, so only the company's accounts can sign in (say why: with External, any Google account could), then an OAuth client ID of type Web application with the redirect URI. The issuer is https://accounts.google.com. Google puts no groups in its tokens, so an app is open to anyone at the company.
     - Okta: an OIDC Web application with the sign-in redirect URI. The issuer is https://<their Okta domain>/oauth2/default. For groups, add a groups claim to that authorization server, and groups to the scopes under More.
     - authentik: an OAuth2/OpenID provider (confidential) and an application for it, with the redirect URI. The issuer is https://<authentik's address>/application/o/<application slug>/. Groups come in the profile scope already.
     - Keycloak: a confidential OpenID Connect client in the realm, with the redirect URI. The issuer is https://<Keycloak's address>/realms/<realm>. For groups, a Group Membership mapper with the token claim name groups and Full group path off.
     - Pocket ID: an OIDC client with the callback URL. The issuer is Pocket ID's address. Add groups to the scopes under More.
     - Another provider: the same pieces by its own names; say plainly where you are unsure of its screens.
  3. If it has none, ask whether they want one. For a provider of their own, explain the two in a few lines: Pocket ID is light and simple, sign-in with passkeys only and no passwords, people and groups in a small admin page, for small teams; authentik is a full identity system (passwords, two-factor, passkeys, policies, a big admin page) for companies that want control, and needs about 2 GB of the server's memory. Commitarium cannot install either on the server yet: they run it themselves, and you help, then connect it as in 2. If they want something else, ask what and help with that.
  4. Never ask for the client secret in this chat, and if they paste one, tell them to enter it in Settings instead and to make a new one, since this chat keeps it.`
