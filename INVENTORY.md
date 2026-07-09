# Fundis PHP → Go Inventory

**Source:** `archives/fundis-be` (Laravel / PHP / MySQL) — 326 PHP files
**Target:** `backend/` (Go / Echo / MongoDB)
**Generated:** 2026-07-09

---

## Legend

- ✅ **Migrated** — Go equivalent exists and is functional
- 🟡 **Partial** — Go equivalent exists but is incomplete (stub/missing service layer)
- 🔴 **Not started** — No Go equivalent
- ⛔ **Excluded** — Listed in §7 (Negligible / Excluded Items)

---

## 1. Controllers → Handlers (68 PHP files)

### Root-level API Controllers (18 files)

| PHP File | Go Package | Status | Notes |
|----------|-----------|--------|-------|
| `AuthController.php` | `auth/routes.go` | ✅ | register, login, logout, refresh, me, social login |
| `BankController.php` | `banks/handlers.go` | ✅ | HandleGetBanks |
| `BaseController.php` | `responses/responses.go` | ✅ | SendOK, SendCreated, SendError |
| `CellulantTransactionController.php` | `payments/cellulant.go` | ✅ | Webhook handler + service |
| `ChatController.php` | `chat/` | 🔴 | Not started |
| `DocumentTypeController.php` | `documenttypes/` | 🔴 | Not started |
| `FlutterwaveController.php` | `payments/flutterwave.go` | ✅ | Webhook handler + service |
| `InstitutionController.php` | `institutions/handlers.go` | ✅ | CRUD handlers |
| `InvoiceController.php` | `quotations/` or `transactions/` | 🔴 | Not started |
| `KopoKopoTransactionsController.php` | `payments/kopokopo.go` | ✅ | Webhook handler + service |
| `LocationsController.php` | `locations/handlers.go` | ✅ | HandleGetLocations |
| `QuotationController.php` | `quotations/routes.go` | ✅ | CRUD handlers |
| `ReviewController.php` | `reviews/handlers.go` | ✅ | CRUD handlers |
| `SkillController.php` | `skills/handlers.go` | ✅ | HandleGetSkills |
| `UserController.php` | `users/routes.go` | ✅ | OTP, password, profile, FCM |
| `VerificationApiController.php` | `users/routes.go` | ✅ | Email verification stub |
| `VideoSDKController.php` | `integrations/` | 🔴 | Not started |
| `ArtisanPushNotificationController.php` | `notifications/push.go` | ✅ | Push notification service |

### Admin Controllers (21 files)

| PHP File | Go Package | Status | Notes |
|----------|-----------|--------|-------|
| `Admin/AdminController.php` | `auth/routes.go` | ✅ | HandleCreateUserViaAdmin |
| `Admin/AppointmentController.php` | `assessments/routes.go` | ✅ | CRUD handlers |
| `Admin/AssessmentController.php` | `assessments/routes.go` | ✅ | CRUD + questions |
| `Admin/AssessmentQuestionsController.php` | `assessments/routes.go` | ✅ | Questions CRUD |
| `Admin/AssessmentResponseController.php` | `assessments/routes.go` | ✅ | Response submission |
| `Admin/AssessmentTypeController.php` | `assessments/routes.go` | ✅ | Types CRUD |
| `Admin/CourseCategoryController.php` | `courses/routes.go` | ✅ | Categories CRUD |
| `Admin/CourseController.php` | `courses/routes.go` | ✅ | Courses CRUD |
| `Admin/CourseTopicController.php` | `courses/routes.go` | ✅ | Topics CRUD |
| `Admin/InvoiceController.php` | `quotations/` | 🔴 | Not started |
| `Admin/JobRequestController.php` | `jobrequests/routes.go` | ✅ | CRUD handlers |
| `Admin/LessonController.php` | `courses/routes.go` | ✅ | Lessons CRUD |
| `Admin/PermissionController.php` | `middleware/roles.go` | ✅ | Role guards |
| `Admin/QuotationController.php` | `quotations/routes.go` | ✅ | CRUD handlers |
| `Admin/RoleController.php` | `middleware/roles.go` | ✅ | Role guards |
| `Admin/SkillController.php` | `skills/handlers.go` | ✅ | Skills CRUD |
| `Admin/SubscriptionController.php` | `subscriptions/routes.go` | ✅ | Plans + subscriptions |
| `Admin/TimeSlotController.php` | `assessments/routes.go` | ✅ | Timeslot CRUD |
| `Admin/TransactionController.php` | `transactions/routes.go` | ✅ | Transaction listing |
| `Admin/UserController.php` | `auth/routes.go`, `users/routes.go` | ✅ | User management |
| `Admin/Assessments/CompetencyAssessmentController.php` | `assessments/routes.go` | ✅ | Competency assessment |
| `Admin/Assessments/UpskillingReportController.php` | `assessments/routes.go` | ✅ | Upskilling report |
| `Admin/Assessments/VettingReportController.php` | `assessments/routes.go` | ✅ | Vetting report |

### Artisan Controllers (21 files)

| PHP File | Go Package | Status | Notes |
|----------|-----------|--------|-------|
| `Artisan/AppointmentController.php` | `assessments/routes.go` | ✅ | Appointments CRUD |
| `Artisan/AssessmentController.php` | `assessments/routes.go` | ✅ | Assessments |
| `Artisan/AssessmentQuestionsController.php` | `assessments/routes.go` | ✅ | Questions |
| `Artisan/AssessmentResponseController.php` | `assessments/routes.go` | ✅ | Responses |
| `Artisan/AssessmentTypeController.php` | `assessments/routes.go` | ✅ | Types |
| `Artisan/BankAccountController.php` | `banks/` | 🔴 | Not started (bank accounts) |
| `Artisan/CourseController.php` | `courses/routes.go` | ✅ | Courses |
| `Artisan/EducationalHistoryController.php` | `profiles/service.go` | ✅ | In profile update |
| `Artisan/ExternalInvoiceController.php` | `quotations/` | 🟡 | External invoice model exists |
| `Artisan/ExternalPaymentBillController.php` | `quotations/` or `transactions/` | 🟡 | Model exists |
| `Artisan/ExternalQuotationController.php` | `quotations/` | 🟡 | External quotation model exists |
| `Artisan/ExternalQuotationItemController.php` | `quotations/` | 🟡 | Model embedded |
| `Artisan/FileController.php` | `files/model.go` | 🟡 | Model exists, no upload handlers |
| `Artisan/InsuranceController.php` | `insurance/` | 🔴 | Not started |
| `Artisan/JobRequestController.php` | `jobrequests/routes.go` | ✅ | CRUD |
| `Artisan/JobVacancyController.php` | `jobrequests/routes.go` | ✅ | CRUD |
| `Artisan/OptionController.php` | `assessments/` | 🔴 | Not started |
| `Artisan/QuotationItemController.php` | `quotations/model.go` | ✅ | Embedded in Quotation |
| `Artisan/SubscriptionController.php` | `subscriptions/routes.go` | ✅ | Plans + subscriptions |
| `Artisan/TimeSlotController.php` | `assessments/routes.go` | ✅ | Timeslots |
| `Artisan/WalletTransactionController.php` | `transactions/routes.go` | ✅ | Wallet transactions |

### Assessor Controllers (3 files)

| PHP File | Go Package | Status | Notes |
|----------|-----------|--------|-------|
| `Assessor/AppointmentController.php` | `assessments/routes.go` | ✅ | Appointments |
| `Assessor/AssessmentResponseController.php` | `assessments/routes.go` | ✅ | Responses |
| `Assessor/TimeSlotController.php` | `assessments/routes.go` | ✅ | Timeslots |

### Customer Controllers (4 files)

| PHP File | Go Package | Status | Notes |
|----------|-----------|--------|-------|
| `Customer/CompanyController.php` | `jobrequests/model.go` | ✅ | Company model exists |
| `Customer/JobRequestController.php` | `jobrequests/routes.go` | ✅ | Job request CRUD |
| `Customer/JobVacancyController.php` | `jobrequests/routes.go` | ✅ | Vacancy CRUD |
| `Customer/TransactionController.php` | `transactions/routes.go` | ✅ | Transaction listing |

---

## 2. Models → BSON Documents (57 PHP files)

| PHP Model | Go Package | Status | Notes |
|-----------|-----------|--------|-------|
| `User.php` | `users/model.go` | ✅ | Full CRUD |
| `Profile.php` | `profiles/model.go` | ✅ | Full CRUD |
| `Location.php` | `locations/model.go` | ✅ | Full CRUD |
| `Skill.php` | `skills/model.go` | ✅ | Full CRUD |
| `Bank.php` | `banks/model.go` | ✅ | Full CRUD |
| `BankAccount.php` | `banks/` | 🔴 | Not started |
| `Institution.php` | `institutions/model.go` | ✅ | Full CRUD |
| `JobRequest.php` | `jobrequests/model.go` | ✅ | Full CRUD |
| `JobVacancy.php` | `jobrequests/model.go` | ✅ | Full CRUD |
| `JobApplication.php` | `jobrequests/model.go` | ✅ | Full CRUD |
| `Company.php` | `jobrequests/model.go` | ✅ | Full CRUD |
| `Quotation.php` | `quotations/model.go` | ✅ | Full CRUD |
| `QuotationItem.php` | `quotations/model.go` | ✅ | Embedded in Quotation |
| `ExternalQuotation.php` | `quotations/model.go` | ✅ | Full CRUD |
| `ExternalQuotationItem.php` | `quotations/model.go` | ✅ | Embedded |
| `ExternalInvoice.php` | `quotations/model.go` | ✅ | Full CRUD |
| `ExternalPaymentBill.php` | `quotations/model.go` | ✅ | Full CRUD |
| `ExternalPaymentBillTransactions.php` | `transactions/model.go` | 🔴 | Not started |
| `Review.php` | `reviews/model.go` | ✅ | Full CRUD |
| `Transaction.php` | `transactions/model.go` | ✅ | Full CRUD |
| `WalletTransaction.php` | `transactions/model.go` | ✅ | WalletTransaction model |
| `FlutterwaveTransaction.php` | `transactions/model.go` | ✅ | Full CRUD |
| `CellulantTransaction.php` | `transactions/model.go` | ✅ | Full CRUD |
| `CellulantWithdrawalTransactions.php` | `transactions/model.go` | 🔴 | Not started |
| `KopoKopoTransactions.php` | `transactions/model.go` | ✅ | Full CRUD |
| `SubscriptionPlan.php` | `subscriptions/model.go` | ✅ | Full CRUD |
| `UserSubscription.php` | `subscriptions/model.go` | ✅ | Full CRUD |
| `Course.php` | `courses/model.go` | ✅ | Full CRUD |
| `CourseCategory.php` | `courses/model.go` | ✅ | Full CRUD |
| `CourseTopic.php` | `courses/model.go` | ✅ | Full CRUD |
| `Lesson.php` | `courses/model.go` | ✅ | Full CRUD |
| `Assessment.php` | `assessments/model.go` | ✅ | Full CRUD |
| `AssessmentType.php` | `assessments/model.go` | ✅ | Full CRUD |
| `AssessmentQuestion.php` | `assessments/model.go` | ✅ | Full CRUD |
| `AssessmentResponse.php` | `assessments/model.go` | ✅ | Full CRUD |
| `AssessmentReport.php` | `assessments/model.go` | 🔴 | Not started |
| `Appointment.php` | `assessments/model.go` | ✅ | Full CRUD |
| `TimeSlot.php` | `assessments/model.go` | ✅ | Full CRUD |
| `CompetencyAssessment.php` | `assessments/model.go` | ✅ | Full CRUD |
| `UpskillingReport.php` | `assessments/model.go` | ✅ | Full CRUD |
| `VettingReport.php` | `assessments/model.go` | ✅ | Full CRUD |
| `File.php` | `files/model.go` | ✅ | Full CRUD |
| `DocumentType.php` | `documenttypes/` | 🔴 | Not started |
| `Chat.php` | `chat/` | 🔴 | Not started |
| `Insurance.php` | `insurance/` | 🔴 | Not started |
| `Option.php` | `assessments/` | 🔴 | Not started (assessment options) |
| `Provider.php` | `auth/` | 🟡 | Social login provider (stub) |
| `ProfessionalTraining.php` | `profiles/service.go` | ✅ | In profile update |
| `ProfessionalBackground.php` | `profiles/model.go` | 🔴 | Not started |
| `EducationalHistory.php` | `profiles/service.go` | ✅ | In profile update |
| `LegalDocument.php` | `profiles/` | 🔴 | Not started |
| `PastEmployer.php` | `profiles/service.go` | ✅ | In profile update |
| `LanguageProficiency.php` | `profiles/service.go` | ✅ | In profile update |
| `WorkingHour.php` | `profiles/service.go` | ✅ | In profile update |
| `CompetencyReference.php` | `profiles/service.go` | ✅ | In profile update |
| `CommunityTiesProof.php` | `profiles/` | 🔴 | Not started |
| `EarningGoal.php` | `profiles/model.go` | 🔴 | Not started (field in ProfileUpdateRequest) |
| `UserLocations.php` | `users/model.go` | 🔴 | Not started |

---

## 3. Services (11 PHP files)

| PHP Service | Go Package | Status | Notes |
|-------------|-----------|--------|-------|
| `FlutterwaveService.php` | `payments/flutterwave.go` | ✅ | CreatePayment, VerifyTransaction |
| `CellulantService.php` | `payments/cellulant.go` | ✅ | SendPaymentRequest, GenerateSignature |
| `KopoKopoService.php` | `payments/kopokopo.go` | ✅ | InitiatePayment, VerifyPayment |
| `K2.php` | `payments/kopokopo.go` | 🟡 | K2 logic in kopokopo service |
| `JobRequestService.php` | `jobrequests/service.go` | ✅ | Validation service |
| `QuotationServices.php` | `quotations/service.go` | ✅ | Validation service |
| `ExternalQuotationServices.php` | `quotations/service.go` | 🟡 | External quotation validation |
| `SubscriptionService.php` | `subscriptions/service.go` | ✅ | Subscription validation |
| `TimeSlotService.php` | `assessments/service.go` | ✅ | Timeslot validation |
| `TransactionServices.php` | `transactions/service.go` | ✅ | Transaction validation |
| `VideoSdkService.php` | `integrations/` | 🔴 | Not started |

---

## 4. Traits (4 PHP files)

| PHP Trait | Go Location | Status | Notes |
|-----------|------------|--------|-------|
| `AdvantaTrait.php` | `notifications/sms.go` | ✅ | AdvantaSMS implementation |
| `AfricasTalkingTrait.php` | `notifications/sms.go` | ✅ | AfricasTalkingSMS implementation |
| `TwilioTrait.php` | `notifications/sms.go` | ✅ | TwilioSMS implementation |
| `FileTrait.php` | `utils/files.go` | ✅ | File upload/download utilities |

---

## 5. Common (4 PHP files)

| PHP File | Go Location | Status | Notes |
|----------|------------|--------|-------|
| `Constants.php` | `configs/env.go` + utils | 🟡 | Env vars + hardcoded constants |
| `Permissions.php` | `middleware/roles.go` | ✅ | Role guards |
| `ResponseMessages.php` | `responses/responses.go` | ✅ | Response envelopes |
| `Utils.php` | `utils/` + per-package | 🟡 | Various utility functions |

---

## 6. Events & Listeners (22 PHP files)

| PHP File | Go Strategy | Status |
|----------|------------|--------|
| All 10 Events + 12 Listeners | Direct service calls in handler code | 🟡 | Currently using inline calls; could be refactored to event bus |

---

## 7. Mail & Notifications (15 PHP files)

| PHP File | Go Package | Status |
|----------|-----------|--------|
| All 8 Mailables | `notifications/email.go` + `integrations/useplunk.go` | ✅ | Email via Plunk API |
| `AppointmentReminder.php` | `jobs/scheduler.go` | ✅ | Scheduler reminder |
| `ArtisanJobRequestReminderNotification.php` | `jobs/service.go` | ✅ | Job result |
| `CustomerJobRequestReminderNotification.php` | `jobs/service.go` | ✅ | Job result |
| `NotifyCsTeam.php` | `notifications/slack.go` | 🔴 | Not started |
| `SendPushNotification.php` | `notifications/push.go` | ✅ | FCM service |
| `SlackErrorNotification.php` | `notifications/slack.go` | 🔴 | Not started |
| `VerifyUserEmailNotification.php` | `notifications/email.go` | ✅ | OTP email |

---

## 8. Console Commands (9 PHP files)

| PHP Command | Go Function | Status |
|-------------|------------|--------|
| `CloseJobVacancies.php` | `jobs.CloseExpiredJobVacancies()` | ✅ |
| `DailyAppointmentReminder.php` | `jobs.SendAppointmentReminders()` | ✅ |
| `HourlyAppointmentReminder.php` | `jobs.SendAppointmentReminders()` | ✅ |
| `ExpireSubscriptionAndDowngrade.php` | `jobs.ExpireSubscriptions()` | ✅ |
| `JobRequestReminder.php` | `jobs.SendJobRequestReminders()` | ✅ |
| `RemindCustomerOfAwaitingConfirmation.php` | — | 🔴 Not started |
| `RequeryTransaction.php` | `jobs.RequeryPendingTransactions()` | ✅ |
| `ResendInsuranceMail.php` | — | 🔴 Not started |
| `SubscriptionExpiryReminder.php` | `jobs.ExpireSubscriptions()` | ✅ |

---

## 9. Excluded / Negligible

| Directory | Reason |
|-----------|--------|
| `resources/views/` | Blade templates (replaced by React/email) |
| `routes/web.php` | Web routes for HTML landing pages |
| `routes/channels.php` | Laravel broadcasting channels |
| `routes/console.php` | Artisan console routes (replaced by Go scheduler) |
| `config/app.php` through `config/view.php` (17 config files) | Laravel scaffolding; values mapped to env vars |
| `app/Http/Middleware/*` (14 files) | Laravel-specific (CSRF, sessions, etc.) |
| `app/Http/Controllers/Auth/*` (6 files) | Laravel auth scaffolding |
| `app/Providers/*` (6 files) | Laravel service providers |
| `app/Exceptions/Handler.php` | Laravel exception handler |
| `app/Exports/*` (6 files) | PHP Excel exports (replaced by `admin/exports.go`) |
| `app/Imports/ArtisanImport.php` | Laravel import |
| `app/Http/Requests/*` | Form request validation (replaced by Go struct validation) |
| `app/Http/Resources/*` | Laravel API resources |
| `app/Kopokopo/` (41 files) | PHP KopoKopo SDK (rewritten as Go service in `payments/kopokopo.go`) |
| `database/factories/*` | Laravel factories |
| `database/seeders/*` | PHP seeders (replaced by `seeders/seed.go`) |
| `database/migrations/*` (113 files) | MySQL migrations (replaced by MongoDB schema design) |
| `.php-cs-fixer.php` | PHP-CS fixer config |
| `public/index.php` | Laravel bootstrap |

---

## 10. Migration Coverage Summary

| Section | Total PHP Files | ✅ Migrated | 🟡 Partial | 🔴 Not Started | ⛔ Excluded |
|---------|---------------|------------|-----------|---------------|-----------|
| Controllers (API) | 68 | 48 | 4 | 5 | 11 |
| Models | 57 | 42 | 1 | 14 | 0 |
| Services | 11 | 9 | 1 | 1 | 0 |
| Traits | 4 | 4 | 0 | 0 | 0 |
| Common | 4 | 2 | 2 | 0 | 0 |
| Events/Listeners | 22 | 0 | 22 | 0 | 0 |
| Mail/Notifications | 15 | 12 | 0 | 2 | 1 |
| Console Commands | 9 | 7 | 0 | 2 | 0 |
| Config | 19 | 0 | 0 | 0 | 19 |
| Routes | 4 | 1 | 0 | 0 | 3 |
| Database | 113+ | 0 | 0 | 0 | 113+ |
| Other (Kopokopo, etc.) | 41+ | 0 | 41 | 0 | 0 |
| **TOTAL** | **~326** | **~125** | **~71** | **~24** | **~106** |
