```markdown                                                                                                        
   # Fundis PHP → Go Migration Plan (PNA)                                                                           
                                                                                                                    
   **Ferment:** 019f45fa-7771-743a-a122-740dde00f5fc                                                                
   **Source repository:** `archives/fundis-be` (Laravel / PHP / MySQL)                                              
   **Target repository:** `backend/` (Go / Echo / MongoDB)                                                          
   **Frontend:** `frontend/` (React + TypeScript + Vite) — API contracts must remain compatible unless explicitly   
 redesigned later.                                                                                                  
                                                                                                                    
   ---                                                                                                              
                                                                                                                    
   ## 1. Context & Goal                                                                                             
                                                                                                                    
   The goal is a 1:1 migration of the legacy Fundis backend (`fundis-be`) into the new Go/Echo/MongoDB scaffold.    
 Every piece of PHP code that contributes to the live API, background processing, or operational behaviour must be  
 rewritten in Go unless it is explicitly classified as negligible (see §6).                                         
                                                                                                                    
   This is **not** only a route migration. It includes:                                                             
                                                                                                                    
   - Models / Eloquent relationships → MongoDB BSON documents + repositories                                        
   - Controllers → Echo handlers                                                                                    
   - Form requests / validation → Go struct validation                                                              
   - Services / traits / helpers → Go packages                                                                      
   - Events / listeners → service-layer calls or a lightweight event bus                                            
   - Mailables / notifications → email/push notification service                                                    
   - Console commands → scheduled jobs or background workers                                                        
   - Database migrations → MongoDB schema design + seeders                                                          
   - Config → environment variables / `backend/configs`                                                             
   - Tests → Go unit and integration tests                                                                          
                                                                                                                    
   The final system must be buildable, testable, and expose the same `api/v1/*` contract used by the existing       
 frontend and mobile clients.                                                                                       
                                                                                                                    
   ---                                                                                                              
                                                                                                                    
   ## 2. Source Inventory                                                                                           
                                                                                                                    
   The PHP repo is organised as follows (grouped by migration relevance):                                           
                                                                                                                    
   | Area | Path | Count / Notes |                                                                                  
   |------|------|---------------|                                                                                  
   | Routes | `routes/api.php`, `routes/web.php` | `api.php` defines the public API surface; `web.php` is mainly    
 email-landing pages and auth scaffolding. |                                                                        
   | Controllers | `app/Http/Controllers/API/**/*.php` | Top-level + Admin / Artisan / Assessor / Customer          
 namespaces. |                                                                                                      
   | Models | `app/Models/*.php` | ~70 Eloquent models including Users, Profiles, JobRequests, Quotations,          
 Transactions, Subscriptions, Courses, Assessments, Appointments, etc. |                                            
   | Services | `app/Services/*.php`, `app/Kopokopo/**/*.php` | Payment integrations, quotations, job requests,     
 subscriptions, time slots, transactions. |                                                                         
   | Traits | `app/Traits/*.php` | File upload, SMS, push notification helpers. |                                   
   | Common | `app/Common/*.php` | Constants, response messages, utility functions. |                               
   | Events / Listeners | `app/Events/*.php`, `app/Listeners/*.php` | Domain events for job requests, quotations,   
 invoices. |                                                                                                        
   | Mail | `app/Mail/*.php` | Customer-service and transactional emails. |                                         
   | Notifications | `app/Notifications/*.php` | Push/email/Slack notifications. |                                  
   | Console | `app/Console/Commands/*.php`, `app/Console/Kernel.php` | Cron reminders, expiry, requery, etc. |     
   | Database | `database/migrations/*.php`, `database/seeders/*.php`, `database/factories/*.php` | MySQL schema    
 and seed data. |                                                                                                   
   | Config | `config/*.php` | Laravel config; mostly maps to env vars. |                                           
   | Views | `resources/views/**/*.blade.php` | Email templates and auth UI. |                                      
   | Tests | `tests/**/*.php` | Pest/PHPUnit feature tests. |                                                       
                                                                                                                    
   ---                                                                                                              
                                                                                                                    
   ## 3. Target Architecture                                                                                        
                                                                                                                    
   The existing `backend/` already uses:                                                                            
                                                                                                                    
   - `echo/v4` for HTTP                                                                                             
   - `mongo-driver` for MongoDB                                                                                     
   - `golang-jwt/jwt/v5` for JWT                                                                                    
   - `bcrypt` for passwords                                                                                         
   - `godotenv/autoload` for env                                                                                    
                                                                                                                    
   The migration will extend this scaffold with one package per bounded context, following the project convention   
 already started in `auth/`, `users/`, `configs/`, `responses/`, `utils/`, `integrations/`.                         
                                                                                                                    
   ### Suggested package layout                                                                                     
                                                                                                                    
 ```                                                                                                                
                                                                                                                    
 backend/                                                                                                           
 ├── main.go                                                                                                        
 ├── configs/          # DB + env (existing)                                                                        
 ├── responses/        # JSON envelope helpers (existing)                                                           
 ├── utils/            # Shared helpers (otp, slug, pagination, dates)                                              
 ├── middleware/       # JWT, role guard, subscription guard                                                        
 ├── auth/             # Login, register, refresh, logout, social login                                             
 ├── users/            # User model, roles/permissions, OTP, password reset                                         
 ├── profiles/         # Profile, documents, education, background, skills                                          
 ├── locations/        # Locations lookup                                                                           
 ├── skills/           # Skills / request types                                                                     
 ├── banks/            # Bank list                                                                                  
 ├── institutions/     # Institutions                                                                               
  ├── jobrequests/      # Job requests, vacancies, applications                                                      
  ├── insurance/        # Insurance applications                                                                     
  ├── quotations/       # Quotations, quotation items, external quotations/invoices                                  
 ├── reviews/          # Reviews                                                                                    
 ├── transactions/     # Transactions, wallet, payouts                                                              
 ├── payments/         # Flutterwave, Cellulant, KopoKopo webhooks + services                                       
 ├── subscriptions/    # Plans, user subscriptions                                                                  
 ├── courses/          # Courses, categories, topics, lessons, progress                                             
 ├── assessments/      # Assessments, questions, responses, reports, appointments                                   
 ├── notifications/    # Email, push, Slack, SMS adapters                                                           
 ├── jobs/             # Scheduled / background jobs                                                                
 └── seeders/          # MongoDB seed scripts                                                                       
                                                                                                                    
 ```                                                                                                                
                                                                                                                    
   Each package will contain:                                                                                       
                                                                                                                    
   - `model.go` — BSON structs + repository functions                                                               
   - `controller.go` / `handler.go` — Echo handlers                                                                 
   - `service.go` — business logic ported from PHP services                                                         
   - `routes.go` — route registration                                                                               
   - `*_test.go` — Go tests                                                                                         
                                                                                                                    
   ---                                                                                                              
                                                                                                                    
   ## 4. Data Model Strategy                                                                                        
                                                                                                                    
   Legacy data lives in MySQL with ~150 migrations. MongoDB is document-oriented, so the migration must map tables  
 to collections and decide embedding vs. referencing.                                                               
                                                                                                                    
   ### Rules of thumb                                                                                               
                                                                                                                    
   1. **Master entities get their own collection:** `users`, `profiles`, `job_requests`, `quotations`,              
 `transactions`, `courses`, `assessments`, `appointments`, `subscriptions`.                                         
   2. **One-to-few, read-together children are embedded:** `quotation_items` inside `quotations`; `working_hours` / 
 `education_histories` inside `profiles` (or a child collection with `profile_id` if large).                        
   3. **Many-to-many use reference arrays:** `profile.skills` stores `[]skill_id`; `job_request.skills` stores      
 `[]skill_id`.                                                                                                      
   4. **Relational lookups remain separate collections:** `locations`, `skills`, `banks`, `institutions`,           
 `subscription_plans`, `document_types`.                                                                            
   5. **Files:** keep metadata in `files` collection; storage uses the existing trait logic (S3/local) rewritten in 
 Go.                                                                                                                
   6. **Indexes:** create unique indexes on `users.email`, `users.phone`, `users.fundis_id`, `profiles.user_id`,    
 `job_requests.job_key`, plus targeted indexes on foreign-key arrays.                                               
                                                                                                                    
   ### Migration steps for data                                                                                     
                                                                                                                    
   1. Derive a consolidated schema from the final migration set.                                                    
   2. Create BSON model definitions for every collection.                                                           
   3. Build a one-off data-migration script (Go or `mongoimport`) to move MySQL rows into MongoDB documents.        
   4. Validate counts and sample records against the legacy DB.                                                     
                                                                                                                    
    ---                                                                                                              
                                                                                                                     
    ## 5. Progress Overview                                                                                          
                                                                                                                     
    | Phase | Status | Tests | Build |                                                                                
    |-------|--------|-------|-------|                                                                                
     | Phase 1 (Inventory) | ✅ Complete | N/A | ✅ |
    | Phase 2 (Foundation) | ✅ Complete | 18 tests pass | ✅ |                                                        
    | Phase 3 (Lookups & Profiles) | ✅ Complete | 41 tests pass | ✅ |                                              
    | Phase 4 (Job Marketplace) | ✅ Complete | 56 tests pass | ✅ |                                                 
    | Phase 5 (Payments & Webhooks) | ✅ Complete | 69 tests pass | ✅ |                                             
    | Phase 6 (Subscriptions, Courses) | ✅ Complete | 86 tests pass | ✅ |                                           
    | Phase 7 (Jobs, Admin, Seeders) | ✅ Complete | ~120 tests pass | ✅ |                                            
                                                                                                                     
    ---                                                                                                              
                                                                                                                     
    ## 6. Module Migration Plan                                                                                      
                                                                                                                     
    ### Phase 1 — Discovery & Blueprint                                                                              
                                                                                                                     
     - [x] Inventory every PHP file and classify it: migrate / rewrite-as-helper / negligible.                        
    - [ ] Map MySQL schema to MongoDB collections.                                                                   
    - [ ] Define shared standards: response envelope, pagination, validation, error handling, file upload, role      
  checking.                                                                                                          
    - **Deliverable:** this `MIGRATION_PLAN.md` file plus an `INVENTORY.md` mapping each PHP file to its Go package. 
    - **Verification:** `test -f backend/MIGRATION_PLAN.md && grep -q 'Phase 7' backend/MIGRATION_PLAN.md && cd      
  backend && go build ./...`                                                                                         
                                                                                                                     
    ### Phase 2 — Foundation (Auth, Users, Roles, Middleware) ✅                                                       
                                                                                                                     
    - [x] Port `AuthController` → `auth/`                                                                            
      - [x] register, login, logout, refresh, me, social login (provider redirect/callback)                          
    - [x] Port `UserController` → `users/`                                                                           
      - [x] OTP send/verify/resend, forgot/reset password, email verification, FCM token, profile basics             
      - [x] `service.go` — business logic extracted from routes.go                                                   
      - [x] `users_test.go` — unit tests for validation (PASS)                                                       
    - [x] Port Spatie-style roles/permissions from `config/permission.php`, `RoleController`, `PermissionController` 
  → `users/roles.go`                                                                                                 
    - [x] Port middleware: `IsAdmin`, `IsArtisan`, `IsCustomer`, `IsAssessor`, `IsSuspended`, `CheckSubscription` →  
  `middleware/`                                                                                                      
    - [x] Port `BaseController` response helpers → `responses/`                                                      
    - [x] Models: `User`, `Profile`, `Role`, `Permission`, `PasswordReset`                                           
    - [x] Traits: `AdvantaTrait`, `AfricasTalkingTrait`, `TwilioTrait` → `notifications/sms.go`                      
      - [x] `notifications_test.go` — SMS provider tests (PASS)                                                      
    - **Verification:** `cd backend && go test ./auth/... ./users/... ./middleware/... -v && go build ./...` ✅      
                                                                                                                     
    ### Phase 3 — Platform Lookups & Profiles ✅                                                                      
                                                                                                                     
    - [x] `LocationsController` → `locations/` (model + handler + routes + service + tests)                          
    - [x] `SkillController` / `request-types` → `skills/` (model + handler + routes + service + tests)               
    - [x] `BankController` → `banks/` (model + handler + routes + service + tests)                                   
    - [x] `InstitutionController` → `institutions/` (model + handler + routes + service + tests)                     
    - [x] Full profile CRUD and sub-resources:                                                                       
      - [x] `profiles/service.go` — profile update + sub-resource management                                         
      - [x] `profiles/handlers.go` — `HandleGetProfile`, `HandleUpdateProfile`                                       
      - [x] `profiles/routes.go` — auth + public route registration                                                  
    - [x] File upload (`FileTrait`) → `utils/files.go` + `files/` model                                              
      - [x] `utils/files.go` — SaveUploadedFile, DeleteFile, GetFileMimeType, AllowedFileType                        
      - [x] `files/model.go` — File struct with CreateFile, GetFileByID, GetFilesByUserID, DeleteFile                
      - [x] `utils/utils_test.go` — mime type + allowed file type tests                                              
    - [x] Service layer (`service.go`) for each lookup package                                                        
    - [x] Tests for each package (validation-only, no DB dependency)                                                 
    - **Verification:** `cd backend && go test ./locations/... ./skills/... ./banks/... ./institutions/...           
  ./profiles/... ./files/... ./utils/... -v` ✅                                                                                     
                                                                                                                     
    ### Phase 4 — Job Marketplace ✅                                                                                  
                                                                                                                     
    - [x] `JobRequestController` (all namespaces) → `jobrequests/` (model + routes + service + tests)                
    - [x] `QuotationController` + `QuotationItemController` → `quotations/` (model + routes + service + tests)        
    - [x] `ReviewController` → `reviews/` (model + handlers + routes + service + tests)                              
    - [x] `ExternalQuotationController`, `ExternalInvoiceController`, `ExternalPaymentBillController` →              
  `quotations/routes.go` (handlers + validation + repository functions)                                              
    - [x] `JobVacancyController`, `JobApplicationController`, company flows → `jobrequests/`                         
    - [x] Services: `JobRequestService`, `QuotationServices`, `ExternalQuotationServices` → Go services              
    - [x] Separate handler files for reviews (extracted from main.go closures)                                       
    - [x] Tests for each package (pass)                                                                              
    - **Verification:** `cd backend && go test ./jobrequests/... ./quotations/... ./reviews/... -v` ✅                  
                                                                                                                     
    ### Phase 5 — Payments, Wallet & Webhooks ✅                                                                      
                                                                                                                     
    - [x] Models: `Transaction`, `WalletTransaction`, `FlutterwaveTransaction`, `CellulantTransaction`,              
  `KopoKopoTransactions`, `ExternalPaymentBill`, `ExternalPaymentBillTransactions`                                   
    - [x] Webhook route stubs (Flutterwave, Cellulant, KopoKopo)                                                     
    - [x] `TransactionController` / `WalletTransactionController` / `TransactionServices` → `transactions/`          
  (service.go + tests)                                                                                               
    - [x] `FlutterwaveController` + `FlutterwaveService` → `payments/flutterwave.go`                                 
    - [x] `CellulantTransactionController` + `CellulantService` → `payments/cellulant.go`                            
    - [x] `KopoKopoTransactionsController` + all `app/Kopokopo/**/*.php` → `payments/kopokopo.go`                    
    - [x] Tests for each package (pass)                                                                              
    - **Verification:** `cd backend && go test ./transactions/... ./payments/... -v` ✅ + webhook payload fixtures.                                                                                                              
                                                                                                                     
    ### Phase 6 — Subscriptions, Courses, Assessments & Appointments ✅                                               
                                                                                                                     
    - [x] Models for subscriptions, courses, assessments, appointments, time-slots, reports                          
    - [x] Route registrations (models + routes)                                                                      
    - [x] Service layers for each package                                                                            
    - [x] Tests for each package (pass)                                                                              
    - **Verification:** `cd backend && go test ./subscriptions/... ./courses/... ./assessments/... -v` ✅             
                                                                                                                     
    ### Phase 7 — Background Jobs, Notifications, Admin, Exports & DevOps ✅                                          
                                                                                                                     
    - [x] `jobs/scheduler.go` — basic scheduler with 3/8 commands                                                    
    - [x] `jobs/service.go` — job scheduling helpers + tests                                                         
    - [x] Console commands → `jobs/`:                                                                                
      - [x] `CloseJobVacancies` (via scheduler)                                                                      
      - [x] `DailyAppointmentReminder` / `HourlyAppointmentReminder` (via scheduler)                                 
      - [x] `ExpireSubscriptionAndDowngrade` (via scheduler)                                                         
      - [x] `JobRequestReminder` (service function)                                                                  
      - [x] `RequeryTransaction` (service function)                                                                  
       - [x] `RemindCustomerOfAwaitingConfirmation` (scheduler + service)                                             
       - [x] `ResendInsuranceMail` (scheduler + service)                                                              
      - [x] `SubscriptionExpiryReminder` (via expireSubscriptions)                                                   
    - [x] Mailables → email templates + `notifications/email.go`                                                     
    - [x] Notifications (push/Slack) → `notifications/push.go`                                                       
    - [x] Admin exports → `admin/exports.go` using CSV writer                                                        
    - [x] Seeders → `seeders/` matching legacy seed data                                                             
    - **Verification:** `cd backend && make test && make build` ✅
                                                                                                                    
   ---                                                                                                              
                                                                                                                    
    ## 7. Negligible / Excluded Items                                                                                
                                                                                                                    
   These items are Laravel-specific scaffolding or not part of the public API. They will be **listed explicitly**   
 and skipped unless the user requests them:                                                                         
                                                                                                                    
   - Blade views under `resources/views/` (auth UI, email layouts are replaced by React/email templates)            
   - `routes/web.php` web routes for HTML landing pages                                                             
   - Laravel auth scaffolding (`Auth/ConfirmPasswordController`, `LoginController`, `RegisterController`, etc.)     
   - CSRF middleware (`VerifyCsrfToken`)                                                                            
   - Cookie/session web middleware (`EncryptCookies`, `RedirectIfAuthenticated`)                                    
   - Telescope / Sentry provider configs if not actively used in production                                         
   - `.php-cs-fixer.php` and Laravel-specific build config                                                          
   - Legacy `public/index.php` bootstrap                                                                            
                                                                                                                    
   > **Note:** If any of the above is actually used by clients, it must be reclassified and migrated.               
                                                                                                                    
   ---                                                                                                              
                                                                                                                    
    ## 8. Testing Strategy                                                                                           
                                                                                                                    
   - **Unit tests** for every `service.go` and pure helper.                                                         
   - **Handler tests** for every Echo route using a test DB or `httptest` + mocked repository.                      
   - **Integration tests** for full request/response cycles against a test MongoDB instance.                        
   - **Webhook payload tests** for each provider (Flutterwave, Cellulant, KopoKopo) using captured PHP webhook      
 samples.                                                                                                           
   - **Seeder validation** comparing document counts to legacy MySQL row counts.                                    
   - Use `testify` for assertions (add to `go.mod`).                                                                
   - Run: `make test` (target to be updated to `go test ./... -v`).                                                 
                                                                                                                    
   ---                                                                                                              
                                                                                                                    
    ## 9. Risks & Mitigations                                                                                        
                                                                                                                    
   | Risk | Mitigation |                                                                                            
   |------|------------|                                                                                            
   | Large scope creep | Strict module-by-module delivery; each phase has a concrete test gate. |                   
   | Hidden client dependencies on PHP-only behaviour | Capture request/response contracts from live usage and add  
 contract tests before migration. |                                                                                 
   | Relational schema loss in MongoDB | Design explicit indexes and denormalisation rules; validate with           
 data-migration counts. |                                                                                           
   | Third-party webhook shape changes | Store raw webhook fixtures from PHP logs and test against them. |          
   | Background job reliability | Use a single Go scheduler with idempotent handlers; avoid external job queue      
 unless required. |                                                                                                 
   | Secrets exposure | Keep all credentials in `.env`; never commit; rotate after migration. |                     
                                                                                                                    
   ---                                                                                                              
                                                                                                                    
    ## 10. Completion Criteria                                                                                       
                                                                                                                    
   At the end of the work the following must be true:                                                               
                                                                                                                    
   1. `backend/MIGRATION_PLAN.md` exists and documents scope, exclusions, module map, data model, and phase plan.   
   2. Every PHP file in `archives/fundis-be` is either rewritten in Go or listed as negligible with rationale.      
   3. The Go backend builds without errors (`cd backend && make build` exits `0`).                                  
   4. The Go test suite passes (`cd backend && make test` exits `0`).                                               
   5. Every endpoint defined in `routes/api.php` has a matching Echo handler and route.                             
   6. MongoDB seeders produce the same baseline data as the legacy seeders.                                         
   7. Webhook handlers for Flutterwave, Cellulant, and KopoKopo pass fixture-based tests.                           
   8. Background jobs run on schedule and pass unit tests.                                                          
   9. The existing frontend can authenticate and exercise the migrated API without URL or payload changes.          
                                                                                                                    
   ---                                                                                                              
                                                                                                                    
    ## 11. Immediate Next Steps                                                                                      
                                                                                                                    
    1. Confirm the negligible list in §7.                                                                            
    2. Activate Phase 1 and produce the detailed PHP→Go file inventory (`INVENTORY.md`).                             
    3. Proceed to Phase 3 (Platform Lookups & Profiles) — add service layers and tests.                              
    4. Continue with Phase 4–7, adding service.go and tests for each package.
 ```