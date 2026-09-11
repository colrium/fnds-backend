# Slack notifications

Activity and assessment alerts use the shared Block Kit renderer in
`integrations/slack_format.go`. Each message has an event heading, a short
summary, named record links, dashboard actions, and environment/time context.
The top-level `text` provides a readable notification and screen-reader fallback.

Set `ADMIN_DASHBOARD_URL` to the admin origin. Terraform supplies this through
`admin_dashboard_url`, with separate production and staging values. For local
development, use the port where the admin app is running. Missing or invalid
configuration produces readable text without broken links.

| Slack record/action | Admin destination |
| --- | --- |
| Person, employer, admin, assessor | `/users/:id` |
| Vacancy | `/vacancies/:id` |
| Company on a vacancy | The vacancy detail page, which displays its company |
| Application | Its vacancy detail page, which contains the applications list |
| Submitted profile / review action | `/profile-reviews/:userId` |
| Assessment, appointment, assessment review decision | `/assessments/journeys/:id` |
| Review SLA queue action | `/profile-reviews` |
| Possible no-show queue action | `/assessments` |

Action links navigate to authenticated admin pages. Approvals and other changes
remain dashboard operations. Direct links work with incoming webhooks without a
Slack interaction callback endpoint.

Use `MessageField.URL`, `DashboardRecordURL`, and `DashboardAction` when adding an
event. Use `SlackText` for user text embedded in detail rows. Record identifiers
belong in URL paths, never display labels. User names fall back to email or a
readable label when unavailable. Vacancy notifications distinguish the posting
admin from the employer when created on someone else's behalf.

Metadata lookups read only names and emails with a two-second timeout; a lookup
failure does not prevent delivery. Operations alerts display up to ten records
and include a queue link and total when more are affected. No-show alerts are
marked delivered only after Slack accepts the message. A missing-meeting alert
is included in the confirmed booking notification after persistence succeeds.

Verify locally without sending to a real Slack channel:

```sh
go test ./integrations ./notifications ./assessments ./jobs ./jobrequests
```

Tests capture payloads with `httptest` and use mock MongoDB responses. They cover
destinations, missing names, escaping, Slack size limits, batched name resolution,
and delivery failures.
