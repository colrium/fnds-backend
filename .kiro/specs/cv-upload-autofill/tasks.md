# Implementation Plan: CV Upload & Autofill

## Overview

This plan implements the CV upload and autofill feature in incremental steps. It starts with backend infrastructure (document type seeding, CV-specific upload validation, and the new `cvparser` package), then builds the frontend components (CVUploader, autofill state hook, indicators), and finishes by wiring everything together. Each task builds on the previous ones so there is no orphaned code.

## Tasks

- [ ] 1. Backend: CV document type seed and file upload extensions
  - [ ] 1.1 Add CV document type seeder
    - Create a new seeder function `SeedDocumentTypes` in `seeders/seed.go` that upserts a "CV" document type record into the `document_types` collection with a stable ID (e.g., `"cv-document-type"`)
    - Register the seeder in `RunAll()` so it runs on startup
    - _Requirements: 7.1_

  - [ ] 1.2 Add CV-specific upload validation in the files package
    - Extend `files/service.go` to export a `ValidateCVUpload(req UploadRequest) error` function that enforces: MIME type must be `application/pdf` or `application/vnd.openxmlformats-officedocument.wordprocessingml.document`, size must be > 0 and ≤ 10,485,760 bytes
    - This function is called by the presign/complete handlers when the `document_type_id` equals the CV document type ID
    - _Requirements: 1.2, 1.3, 1.4, 7.3, 7.4_

  - [ ] 1.3 Implement CV file replacement logic in the files package
    - Add a `FindByUserAndDocType(ctx, userID, docTypeID) (*File, error)` method to `Repository` and `MongoRepository`
    - In the files `CompleteUpload` flow, when document_type_id is the CV type: look up existing CV record for the user, delete the old S3 object (log and continue on failure), delete the old record, then create the new record
    - Ensure at most one CV file record per artisan
    - _Requirements: 7.5, 7.6_

  - [ ]* 1.4 Write property tests for file validation (Property 1)
    - **Property 1: File validation rejects invalid uploads**
    - Use `rapid` to generate random UploadRequest instances with varied MIME types, sizes (0, 1, 10MB, 10MB+1, MAX_INT), and verify `ValidateCVUpload` correctly rejects/accepts
    - **Validates: Requirements 1.2, 1.3, 1.4, 7.3, 7.4**

- [ ] 2. Backend: CV Parser package — core extraction service
  - [ ] 2.1 Create `cvparser` package with types and interfaces
    - Create `cvparser/types.go` with `ExtractionResult`, `PersonalDetails`, `Education`, `Professional`, `WorkHistoryEntry`, `LanguageEntry`, `ReferenceEntry` structs
    - Create `cvparser/parser.go` with `Parser` interface (`Extract(ctx, pdfBytes) (*ExtractionResult, error)`) and `OpenAIParser` struct
    - Define the OpenAI structured output JSON schema as a constant
    - _Requirements: 2.1, 2.2, 2.3_

  - [ ] 2.2 Implement OpenAI parser with structured output
    - Implement `OpenAIParser.Extract()` — sends PDF content (base64-encoded) to OpenAI GPT-4o-mini using the chat completions API with structured output (response_format), using a 30-second HTTP client timeout
    - Parse the structured JSON response into `ExtractionResult`
    - Handle errors: unreadable PDF, password-protected, timeout, API unavailable
    - _Requirements: 2.1, 2.2, 2.3, 2.4, 2.5, 2.6, 2.7, 2.8_

  - [ ] 2.3 Implement field mapper (`cvparser/mapper.go`)
    - Create `Mapper` struct with `MapToAutofill(result *ExtractionResult) *AutofillPayload` method
    - Map `ExtractionResult` fields to `AutofillPayload` fields with confidence scores
    - Enforce: only known profile fields are included; type-invalid fields omitted (confidence = 0); collections capped at 10 items (sorted by descending confidence); confidence scores bounded [0, 1]
    - _Requirements: 3.1, 3.2, 3.3, 3.6_

  - [ ]* 2.4 Write property tests for field mapper (Properties 2, 3, 4, 7)
    - **Property 2: Field mapper produces only known profile fields**
    - **Property 3: Type-invalid fields are omitted with zero confidence**
    - **Property 4: Collection fields are capped at 10 items**
    - **Property 7: Confidence scores are bounded between 0 and 1**
    - Use `rapid` to generate diverse `ExtractionResult` inputs and verify mapper invariants
    - **Validates: Requirements 3.1, 3.2, 3.3, 3.6, 2.3**

  - [ ] 2.5 Implement CV parse handler (`cvparser/handlers.go`)
    - Create `Handler` struct with dependencies: `files.Repository`, `files.ObjectStorage`, `Parser`, `Mapper`
    - Implement `POST /api/v1/cv/parse` handler: validate request, verify file ownership (object key prefix + user-id metadata), download PDF from S3, call parser, call mapper, return `ParseCVResponse`
    - Define error types in `cvparser/errors.go`
    - _Requirements: 2.1, 6.2, 6.5, 6.6_

  - [ ] 2.6 Register CV parse route in `main.go`
    - Instantiate `cvparser.Handler` with required dependencies (OpenAI API key from env, files repository, S3 store)
    - Register `POST /cv/parse` on the artisan-only route group (requires JWT + IsArtisan middleware)
    - _Requirements: 6.2_

  - [ ]* 2.7 Write property test for ownership validation (Property 10)
    - **Property 10: Ownership validation rejects mismatched user IDs**
    - Use `rapid` to generate random user IDs and object keys, verify the ownership check correctly rejects/accepts
    - **Validates: Requirements 6.2, 6.5**

  - [ ]* 2.8 Write unit tests for CV parser and handler
    - Test parser with mocked OpenAI responses: valid extraction, empty extraction, unreadable PDF, password-protected, timeout
    - Test handler: successful flow, file not found, ownership mismatch, parse errors
    - _Requirements: 2.5, 2.6, 2.7, 2.8, 2.9, 6.5, 6.6_

- [ ] 3. Checkpoint - Ensure all backend tests pass
  - Ensure all tests pass, ask the user if questions arise.

- [ ] 4. Frontend: CV Uploader component
  - [ ] 4.1 Create the CVUploader component
    - Create `frontend/src/components/profile-setup/CVUploader.tsx`
    - Implement state machine: idle → uploading → parsing → success → error
    - Render a drop-zone/button above the step indicator, visible on ≥ 360px viewports
    - Validate file: `.pdf` extension, `application/pdf` MIME type, size ≤ 10MB
    - Display file validation errors inline ("Please upload a valid PDF file", "CV file must be smaller than 10 MB")
    - Show progress indicator during upload; disable upload control during upload
    - On upload success: call `POST /api/v1/cv/parse` with the file_id
    - On parse success: invoke `onParseComplete` callback with the autofill payload
    - On error: display appropriate message, offer retry (up to 3 times without re-selecting file), or "proceed with manual entry"
    - Show success state with file name and "remove" option
    - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5, 1.6, 1.7, 1.8, 5.1, 5.2, 5.3, 5.4, 5.5, 5.6, 5.8_

  - [ ] 4.2 Add CV parse API function
    - Create `parseCV(fileId: string): Promise<ParseCVResponse>` in a new file `frontend/src/api/cvParser.ts`
    - Define TypeScript types for `AutofillPayload`, `FieldWithConfidence`, `NumericFieldWithConfidence`, collection types, and `ParseCVResponse`
    - _Requirements: 2.1, 3.1_

  - [ ]* 4.3 Write unit tests for CVUploader component
    - Test all render states (idle, uploading, parsing, success, error)
    - Test file validation (wrong type, too large, valid)
    - Test retry flow (up to 3 attempts)
    - Test "proceed with manual entry" flow
    - _Requirements: 1.1–1.8, 5.1–5.8_

- [ ] 5. Frontend: Autofill state management and indicators
  - [ ] 5.1 Create `useAutofillState` hook
    - Create `frontend/src/hooks/useAutofillState.ts`
    - Implement `AutofillMetadata` tracking: confidence scores map, autofilled fields set, total count, banner dismissed state
    - Implement `applyAutofill(payload, currentDraft)`: merge autofill into draft — scalar fields only populate empty draft fields; collection fields append (capped at 10 appended items)
    - Implement `markEdited(fieldPath)`: remove field from autofilled set, decrement count
    - Implement `clearAutofill(currentDraft)`: remove only autofilled values, preserve manually entered/edited values
    - Implement `dismissBanner()`
    - _Requirements: 3.4, 3.5, 3.7, 4.1, 4.2, 4.3, 4.4, 4.5_

  - [ ]* 5.2 Write property tests for autofill state (Properties 5, 6, 8, 9)
    - **Property 5: Scalar autofill does not overwrite existing draft values**
    - **Property 6: Collection autofill appends without replacing**
    - **Property 8: Editing an autofilled field removes it from the autofill set**
    - **Property 9: Clear autofill preserves manually edited values**
    - Use `fast-check` to generate draft states and autofill payloads, verify invariants
    - **Validates: Requirements 3.5, 4.2, 4.4**

  - [ ] 5.3 Create AutofillIndicator component
    - Create `frontend/src/components/profile-setup/AutofillIndicator.tsx`
    - Render a small icon badge next to autofilled fields
    - Show warning icon + tooltip ("This value needs review") when confidence < 0.6
    - Show neutral autofill indicator when confidence ≥ 0.6
    - Hide when field is no longer in autofilled set (after edit)
    - _Requirements: 3.7, 4.1, 4.2_

  - [ ] 5.4 Create AutofillSummaryBanner component
    - Create `frontend/src/components/profile-setup/AutofillSummaryBanner.tsx`
    - Display count of auto-filled fields across all steps
    - Visible on every wizard step until dismissed
    - Include "Clear autofill" action with confirmation prompt
    - _Requirements: 4.3, 4.4, 4.5_

  - [ ]* 5.5 Write unit tests for AutofillIndicator and AutofillSummaryBanner
    - Test indicator visibility based on confidence thresholds
    - Test banner count display, dismiss behavior, clear autofill confirmation/cancel
    - _Requirements: 3.7, 4.3, 4.4, 4.5_

- [ ] 6. Checkpoint - Ensure all frontend tests pass
  - Ensure all tests pass, ask the user if questions arise.

- [ ] 7. Frontend: Wire CVUploader and autofill into ProfileSetupPage
  - [ ] 7.1 Integrate CVUploader into ProfileSetupPage
    - Import and render `CVUploader` above the step indicator in `ProfileSetupPage.tsx`
    - Connect `onParseComplete` to call `useAutofillState.applyAutofill` and update the draft
    - Connect `onError` to display error messages via the existing error handling pattern
    - Ensure artisan can skip upload and proceed to manual entry (no blocking)
    - _Requirements: 1.1, 1.8, 5.6, 5.7_

  - [ ] 7.2 Integrate autofill state into profile wizard steps
    - Pass autofill metadata into each profile step component
    - Render `AutofillIndicator` next to autofilled fields in Steps 1–6
    - Call `markEdited` when artisan modifies an autofilled field value (remove autofill indicator)
    - Render `AutofillSummaryBanner` on every step when autofill is active
    - Ensure autofilled data is NOT auto-saved — artisan must explicitly save each step
    - _Requirements: 3.4, 3.5, 3.7, 3.8, 4.1, 4.2, 4.3_

  - [ ]* 7.3 Write integration tests for end-to-end autofill flow
    - Test: upload CV → parse succeeds → fields populated across steps
    - Test: low confidence fields show warning indicators
    - Test: edit field removes autofill indicator
    - Test: clear autofill removes only autofilled values
    - Test: error during parse → artisan can proceed with manual entry
    - _Requirements: 1.1–1.8, 3.4–3.8, 4.1–4.5, 5.1–5.7_

- [ ] 8. Final checkpoint - Ensure all tests pass
  - Ensure all tests pass, ask the user if questions arise.

## Notes

- Tasks marked with `*` are optional and can be skipped for faster MVP
- Each task references specific requirements for traceability
- Checkpoints ensure incremental validation
- Property tests validate universal correctness properties from the design document
- Unit tests validate specific examples and edge cases
- Backend uses Go with `rapid` library for property-based testing
- Frontend uses TypeScript with `fast-check` library for property-based testing
- The `cvparser` package is entirely new and self-contained — it depends on the existing `files` package interfaces
- The frontend autofill state is a new layer on top of the existing `useProfileSetup` hook pattern

## Task Dependency Graph

```json
{
  "waves": [
    { "id": 0, "tasks": ["1.1", "1.2", "4.2"] },
    { "id": 1, "tasks": ["1.3", "1.4", "2.1", "4.1"] },
    { "id": 2, "tasks": ["2.2", "2.3", "4.3", "5.1"] },
    { "id": 3, "tasks": ["2.4", "2.5", "5.2", "5.3", "5.4"] },
    { "id": 4, "tasks": ["2.6", "2.7", "2.8", "5.5"] },
    { "id": 5, "tasks": ["7.1"] },
    { "id": 6, "tasks": ["7.2"] },
    { "id": 7, "tasks": ["7.3"] }
  ]
}
```
