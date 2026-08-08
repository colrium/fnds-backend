# Requirements Document

## Introduction

This feature enables artisans on the Fundis platform to upload a PDF CV at the beginning of the professional profile setup process. The system parses the uploaded CV, extracts relevant information, and auto-fills the profile fields across all six steps of the profile wizard. This reduces manual data entry, improves profile completion rates, and provides a faster onboarding experience for artisans who already have a prepared CV.

## Glossary

- **Artisan**: A service professional who registers on the Fundis platform to offer their skills to clients.
- **CV_Uploader**: The frontend component that allows an artisan to select and upload a PDF CV file before beginning manual profile entry.
- **CV_Parser**: The backend service responsible for extracting structured data from a raw PDF CV document.
- **Profile_Autofill_Service**: The backend service that maps parsed CV data to Fundis profile fields and returns a structured autofill payload.
- **Profile_Setup_Wizard**: The existing 6-step frontend form that guides the artisan through creating their professional profile (Personal, Experience, References, Next of Kin, Locations, Working Hours).
- **Upload_Service**: The existing backend file upload infrastructure that handles presigned S3 URL generation, direct upload, and completion verification.
- **Extraction_Result**: The structured JSON response returned by the CV_Parser containing all fields successfully extracted from the CV.
- **Autofill_Payload**: The subset of Extraction_Result fields that map directly to ProfileUpdateRequest fields, formatted for the frontend draft state.
- **Confidence_Score**: A numeric value between 0 and 1 indicating the CV_Parser's certainty that an extracted field value is correct.

## Requirements

### Requirement 1: CV File Upload

**User Story:** As an artisan, I want to upload my existing CV as a PDF file before starting the profile wizard, so that I can avoid manually typing information that already exists in my CV.

#### Acceptance Criteria

1. WHEN the artisan navigates to the Profile Setup page, THE CV_Uploader SHALL display an upload option for a PDF CV above the step indicator, rendered as a distinct drop-zone or button that is visible without scrolling on viewports 360 px wide or larger.
2. WHEN the artisan selects a PDF file for upload, THE CV_Uploader SHALL validate that the file has a `.pdf` extension and `application/pdf` MIME type before initiating the upload.
3. WHEN the artisan selects a file that is not a valid PDF, THE CV_Uploader SHALL display an error message stating "Please upload a valid PDF file" and SHALL NOT initiate the upload.
4. WHEN the artisan selects a PDF file larger than 10 MB, THE CV_Uploader SHALL display an error message stating "CV file must be smaller than 10 MB" and SHALL NOT initiate the upload.
5. WHEN the artisan selects a valid PDF file, THE CV_Uploader SHALL initiate the upload using the existing presigned URL flow provided by the Upload_Service and, upon successful completion, SHALL display a success confirmation showing the uploaded file name and a remove option.
6. WHILE the CV file is uploading, THE CV_Uploader SHALL display a progress indicator showing that the upload is in progress and SHALL disable the upload control to prevent concurrent uploads.
7. IF the upload fails due to a network or server error, THEN THE CV_Uploader SHALL display an error message indicating that the upload could not be completed and SHALL allow the artisan to retry without re-selecting the file.
8. IF the artisan has not uploaded a CV, THEN THE Profile_Setup_Wizard SHALL allow the artisan to skip the upload and proceed directly to manual profile entry.

### Requirement 2: CV Parsing

**User Story:** As an artisan, I want the system to read and understand my CV, so that the relevant information is extracted accurately for my profile.

#### Acceptance Criteria

1. WHEN a CV file upload completes successfully, THE Profile_Autofill_Service SHALL send the uploaded PDF to the CV_Parser for text extraction and field parsing.
2. THE CV_Parser SHALL extract the following field categories from the PDF content: personal details (address, city, state), education (highest level, certificate, institution, other qualifications), professional information (professional body, skill acquisition mode, years of experience), work history (employer name, phone number, location), language proficiencies (language, proficiency level, written skills), and references (name, phone number, job title, email, location).
3. WHEN the CV_Parser extracts a field value, THE CV_Parser SHALL assign a Confidence_Score between 0 and 1 (inclusive, to two decimal places) to that field indicating extraction certainty.
4. IF the CV_Parser cannot locate a value for an extractable field in the PDF content, THEN THE CV_Parser SHALL omit that field from the Extraction_Result rather than returning a placeholder or empty value.
5. WHEN the CV_Parser cannot extract any text from the uploaded PDF (including scanned image-only PDFs without an embedded text layer), THE Profile_Autofill_Service SHALL return an error response indicating the PDF could not be read.
6. WHEN the CV_Parser encounters a password-protected PDF, THE Profile_Autofill_Service SHALL return an error response indicating the PDF is password-protected and cannot be processed.
7. THE CV_Parser SHALL complete parsing of a single CV document within 30 seconds.
8. IF the CV_Parser processing exceeds 30 seconds, THEN THE Profile_Autofill_Service SHALL return a timeout error to the frontend.
9. WHEN the CV_Parser extracts zero mappable fields from a valid, text-readable PDF, THE Profile_Autofill_Service SHALL return an Extraction_Result with an empty field set and no error status.

### Requirement 3: Profile Field Mapping and Autofill

**User Story:** As an artisan, I want my CV data to automatically fill in the matching profile fields, so that I can review and adjust rather than type everything from scratch.

#### Acceptance Criteria

1. WHEN the CV_Parser returns an Extraction_Result, THE Profile_Autofill_Service SHALL map each extracted field that has a corresponding ProfileUpdateRequest field and discard any extracted fields that do not match a supported profile field.
2. THE Profile_Autofill_Service SHALL map extracted data to the following profile fields: address, city, state, highest_education_level, certificate, institution, other_qualifications, professional_body, skill_acquisition_mode, years_of_experience, past_employers (name, phone_number, location), language_proficiencies (language, proficiency_level, written_skills), and references (name, phone_number, job_title, email, location).
3. IF an extracted field value does not conform to the expected data type or format of its target ProfileUpdateRequest field (e.g., a non-numeric value for years_of_experience), THEN THE Profile_Autofill_Service SHALL omit that field from the Autofill_Payload and assign it a Confidence_Score of 0.
4. WHEN the Profile_Autofill_Service returns the Autofill_Payload to the frontend, THE Profile_Setup_Wizard SHALL populate the corresponding form fields across all six steps with the extracted values.
5. THE Profile_Setup_Wizard SHALL only autofill scalar fields (e.g., address, city) that have no user-entered value in the artisan's draft; for collection fields (past_employers, language_proficiencies, references), THE Profile_Setup_Wizard SHALL append extracted items to any existing entries without replacing them.
6. THE Profile_Autofill_Service SHALL include a maximum of 10 items per collection field (past_employers, language_proficiencies, references) in the Autofill_Payload.
7. WHEN a field has a Confidence_Score below 0.6, THE Profile_Setup_Wizard SHALL display a distinguishable warning indicator adjacent to that field's input and a tooltip stating that the value needs review.
8. THE Profile_Setup_Wizard SHALL NOT automatically save autofilled data to the server; the artisan must explicitly save each step.

### Requirement 4: Artisan Review and Edit of Autofilled Data

**User Story:** As an artisan, I want to review and edit any auto-filled information before saving, so that I can correct inaccuracies or add missing details.

#### Acceptance Criteria

1. WHEN the Profile_Setup_Wizard populates fields with autofilled data, THE Profile_Setup_Wizard SHALL display all autofilled fields as editable form inputs pre-populated with the extracted values and visually distinguished from empty fields by an autofill indicator.
2. WHEN the artisan modifies an autofilled field value, THE Profile_Setup_Wizard SHALL use the artisan's manual input as the authoritative value for that field and remove the autofill indicator from that field.
3. WHEN autofilled data is loaded into the draft, THE Profile_Setup_Wizard SHALL display a summary banner indicating how many fields were auto-filled across all steps, and the banner SHALL remain visible on every wizard step until dismissed by the artisan.
4. WHEN the artisan activates the "Clear autofill" action, THE Profile_Setup_Wizard SHALL present a confirmation prompt before clearing, and upon confirmation SHALL remove only the autofilled values from all steps, preserving any values the artisan has manually entered or edited.
5. IF the artisan cancels the "Clear autofill" confirmation prompt, THEN THE Profile_Setup_Wizard SHALL retain all current field values unchanged.

### Requirement 5: Error Handling and Feedback

**User Story:** As an artisan, I want clear feedback when the CV upload or parsing fails, so that I understand what happened and can take corrective action.

#### Acceptance Criteria

1. IF the file upload fails due to a network error, THEN THE CV_Uploader SHALL display an error message indicating the upload could not be completed, offer a retry option allowing up to 3 consecutive retry attempts, and retain the artisan's selected file so they do not need to re-select it.
2. IF the CV_Parser returns an error indicating the PDF is unreadable, THEN THE Profile_Setup_Wizard SHALL display a message indicating that the PDF content could not be read, and present the artisan with options to upload a different PDF or proceed with manual entry.
3. IF the CV_Parser returns an error indicating the PDF is password-protected, THEN THE Profile_Setup_Wizard SHALL display a message asking the artisan to upload an unprotected version of the CV, and return the CV_Uploader to its ready state so the artisan can select a new file.
4. IF the parsing request times out, THEN THE Profile_Setup_Wizard SHALL display a message indicating the CV could not be processed within 30 seconds, and present the artisan with options to retry parsing or proceed with manual entry.
5. WHEN the CV_Parser extracts zero mappable fields from a valid PDF, THE Profile_Setup_Wizard SHALL inform the artisan that no relevant profile data was found and suggest proceeding with manual entry.
6. IF the Profile_Autofill_Service returns a server error, THEN THE Profile_Setup_Wizard SHALL display an error message indicating the service is temporarily unavailable and allow the artisan to continue with manual profile entry.
7. IF any error occurs during CV upload or parsing, THEN THE Profile_Setup_Wizard SHALL preserve all previously entered draft data and any previously autofilled field values that have already been saved by the artisan.
8. IF any error occurs during CV upload or parsing, THEN THE CV_Uploader SHALL replace the progress indicator with the error message within 2 seconds of error detection.

### Requirement 6: Security and Data Privacy

**User Story:** As an artisan, I want my uploaded CV to be handled securely, so that my personal information is protected.

#### Acceptance Criteria

1. THE Upload_Service SHALL store CV files in S3 with the same user-scoped object key pattern used by the existing file upload infrastructure, ensuring only the owning artisan can access the file.
2. THE Profile_Autofill_Service SHALL verify ownership of the uploaded CV file by confirming that the object key starts with the authenticated artisan's user-scoped prefix and that the object's user-id metadata matches the authenticated artisan's identity before initiating parsing.
3. THE CV_Parser SHALL NOT persist extracted text or structured data beyond the duration of the parsing request, and SHALL enforce a maximum parsing duration of 60 seconds, after which any in-memory data is discarded.
4. THE Profile_Autofill_Service SHALL transmit the Autofill_Payload exclusively over HTTPS.
5. IF the CV file's user-id metadata does not match the authenticated artisan's identity, THEN THE Profile_Autofill_Service SHALL reject the parsing request and return an error response indicating an ownership mismatch without revealing the file's actual owner.
6. IF the referenced CV file does not exist in S3 or has been deleted at the time parsing is requested, THEN THE Profile_Autofill_Service SHALL reject the request and return an error response indicating the file was not found.

### Requirement 7: File Storage and Lifecycle

**User Story:** As a platform operator, I want uploaded CVs to be managed within the existing file infrastructure, so that storage is consistent and maintainable.

#### Acceptance Criteria

1. THE Upload_Service SHALL store the uploaded CV with a document_type_id that references a document type designated for CV documents in the document_types collection, distinct from other uploaded file types.
2. WHEN the CV upload and completion flow finishes, THE Upload_Service SHALL create a file record in the files collection with user_id matching the authenticated artisan, file_name matching the original upload filename, file_url pointing to the S3 object location, mime_type matching the uploaded file's content type, and size matching the uploaded file's byte length.
3. IF a CV upload request specifies a file size exceeding 10 MB (10,485,760 bytes) or a size of 0 bytes, THEN THE Upload_Service SHALL reject the request with an error indicating the allowed size range and SHALL NOT store any file or create any record.
4. THE Upload_Service SHALL accept only application/pdf and application/vnd.openxmlformats-officedocument.wordprocessingml.document mime types for CV uploads.
5. IF an artisan uploads a new CV and a previous CV file record exists for that artisan, THEN THE Upload_Service SHALL delete the old S3 object and replace the previous file record with the new CV file record, ensuring the artisan has at most one CV file record at any time.
6. IF the deletion of the old S3 object fails during CV replacement, THEN THE Upload_Service SHALL still complete the new CV record creation and log the orphaned S3 object for later cleanup.
