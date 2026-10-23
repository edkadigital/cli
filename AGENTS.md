# Edka CLI development

Use Go and keep the binary portable across macOS, Linux and Windows. Follow the
existing Cobra command groups, context precedence and output contracts. Keep
credentials separate from profiles and project links. Never weaken TLS, redirect,
OAuth state/issuer/PKCE, audience, scope, membership or confirmation checks.

Run `make test` and `make check` for behavior changes, and `make skill` after a
change to a command's help, which the agent skill in `skills/edka` repeats.
`skills/edka-app` is a copy of the skill in edkadigital/apps: change it there, then
run `make app-skill` here. A release fails when the copy is older. Keep
machine output on stdout and progress and errors on stderr. Report what the API
confirmed, never an optimistic success message.

`internal/catalog/operations.json` is generated from the Edka API's source, outside
this repository. Never edit it by hand. A new endpoint, request body schema or
confirmation mark arrives as a regenerated file. Commands under `edka api` use the
routes the catalog lists, never placeholder endpoints.
