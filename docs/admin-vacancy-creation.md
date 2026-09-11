# Admin vacancy creation

These endpoints are registered on `/api/v1/admin` behind `JWTAuth` and `IsAdmin`.
They assist existing users; they never create user accounts or change account types.
The selected account may be any existing user type.

| Method | Path (relative to `/api/v1/admin`) | Purpose |
|---|---|---|
| GET | `/vacancies/options` | Supported trades, job levels, and employment types. |
| GET | `/users/:id/companies` | Company profiles owned by an existing user. |
| POST | `/users/:id/companies` | Save a company profile for an existing user. |
| POST | `/vacancies` | Publish a vacancy using the owner's saved company. |

Company creation accepts `name`, `website`, `location`, `contact_person`,
`contact_person_phone`, and `contact_person_email`. Name, contact person, and contact
phone are required; email and website are validated when supplied. Admins may create
additional company profiles for a user, unlike the self-service one-company limit.
An existing company with the same case-insensitive name for that owner returns 409.

Vacancy creation accepts:

```json
{
  "owner_id": "existing-user-id",
  "company_id": "company-owned-by-that-user",
  "title": "Carpenter",
  "level": "MID_LEVEL",
  "positions": 2,
  "employment_type": "FULL_TIME",
  "experience": "2 years",
  "salary_minimum": 20000,
  "salary_maximum": 40000,
  "skill": "Carpenter",
  "location": "Nakuru",
  "description": "Build and install fitted cabinets.",
  "responsibilities": "Measure, assemble and fit cabinets.",
  "application_start_date": "2026-09-15",
  "application_end_date": "2026-10-15"
}
```

Salary amounts are positive whole monthly amounts in KES. The stored `salary_range`
keeps the existing `minimum-maximum` format. Dates are optional `YYYY-MM-DD` inputs
and are stored as RFC3339 strings. The end date cannot precede the start date.

The backend checks that the user exists and resolves the company using both
`company_id` and `owner_id`. Missing users and unavailable or differently owned
companies return 404. Invalid input returns 400; storage failures return 500.

Server-controlled fields include generated IDs, `owner_id` on companies, `created_by`,
timestamps, status, featured state, workspace assignment, and company snapshots.
New vacancies are open and not featured. The company supplies its snapshot and, if
present, its workspace ID; the administrator's workspace is never assigned.

Company creation and vacancy publication are separate explicit saves. A saved company
remains available if publication fails or is cancelled. Both records store the selected
user in `owner_id` and the acting admin in `created_by`. Publication runs the same
matching-worker and activity notifications as employer-created vacancies, after the
vacancy is persisted.

Verification: `go test ./adminapi ./jobrequests -race` covers ownership, missing
accounts, additional companies, validation, storage failures, notification ordering,
and the non-admin authorization boundary.
