package assessments

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"strings"
	"time"

	"backend/configs"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

const collectionCertificates = "assessment_certificates"

// CertificateValidityYears is how many years a certificate is valid.
const CertificateValidityYears = 2

// Certificate records an issued certification. The verification_code is the
// public-facing lookup key so the actual document ID is never exposed.
type Certificate struct {
	ID               string    `json:"id" bson:"_id"`
	UserID           string    `json:"user_id" bson:"user_id"`
	JourneyID        string    `json:"journey_id" bson:"journey_id"`
	SkillID          string    `json:"skill_id" bson:"skill_id"`
	ArtisanName      string    `json:"artisan_name" bson:"artisan_name"`
	FundisID         string    `json:"fundis_id" bson:"fundis_id"`
	Grade            int       `json:"grade" bson:"grade"`
	GradeDescriptor  string    `json:"grade_descriptor" bson:"grade_descriptor"`
	VerificationCode string    `json:"verification_code" bson:"verification_code"`
	S3Key            string    `json:"s3_key,omitempty" bson:"s3_key,omitempty"`
	IssuedAt         time.Time `json:"issued_at" bson:"issued_at"`
	ExpiresAt        time.Time `json:"expires_at" bson:"expires_at"`
	CreatedAt        time.Time `json:"created_at" bson:"created_at"`
}

// GradeDescriptorFor returns a human-readable label for a numeric grade.
func GradeDescriptorFor(grade int) string {
	switch {
	case grade >= 90:
		return "Distinction"
	case grade >= 75:
		return "Merit"
	case grade >= 60:
		return "Pass"
	default:
		return "Ungraded"
	}
}

// generateVerificationCode creates a random 12-character hex code for public
// certificate lookups. Collision probability is negligible at this length.
func generateVerificationCode() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating verification code: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// IssueCertificate creates a certificate record when an artisan completes the
// practical stage. It is idempotent: if a certificate already exists for the
// journey, it returns that one instead of creating a duplicate.
func IssueCertificate(ctx context.Context, journey *Journey, artisanName, fundisID string, grade int) (*Certificate, error) {
	// Check for existing certificate.
	existing, err := GetCertificateByJourney(ctx, journey.ID)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	code, err := generateVerificationCode()
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	cert := &Certificate{
		ID:               uuid.New().String(),
		UserID:           journey.UserID,
		JourneyID:        journey.ID,
		SkillID:          journey.SkillID,
		ArtisanName:      artisanName,
		FundisID:         fundisID,
		Grade:            grade,
		GradeDescriptor:  GradeDescriptorFor(grade),
		VerificationCode: code,
		IssuedAt:         now,
		ExpiresAt:        now.AddDate(CertificateValidityYears, 0, 0),
		CreatedAt:        now,
	}

	if _, err := configs.MI.DB.Collection(collectionCertificates).InsertOne(ctx, cert); err != nil {
		// The check above cannot close the race on its own: two concurrent
		// approvals both see "not found" and both insert. The unique index on
		// journey_id makes the second insert fail, which is the desired outcome,
		// but the caller wants the certificate rather than an error — so return
		// whichever one won.
		if mongo.IsDuplicateKeyError(err) {
			existing, getErr := GetCertificateByJourney(ctx, journey.ID)
			if getErr == nil {
				return existing, nil
			}
			// A duplicate on verification_code rather than journey_id: the code
			// collided, so there is no certificate for this journey to return.
			// Surface the original error rather than the lookup miss.
			return nil, fmt.Errorf("creating certificate: %w", err)
		}
		return nil, fmt.Errorf("creating certificate: %w", err)
	}
	return cert, nil
}

// GetCertificateByJourney loads the certificate for a journey.
func GetCertificateByJourney(ctx context.Context, journeyID string) (*Certificate, error) {
	var cert Certificate
	err := configs.MI.DB.Collection(collectionCertificates).
		FindOne(ctx, bson.M{"journey_id": journeyID}).
		Decode(&cert)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("getting certificate by journey: %w", err)
	}
	return &cert, nil
}

// GetCertificateByCode looks up a certificate by its public verification code.
// This is the function backing the public verification endpoint.
func GetCertificateByCode(ctx context.Context, code string) (*Certificate, error) {
	var cert Certificate
	err := configs.MI.DB.Collection(collectionCertificates).
		FindOne(ctx, bson.M{"verification_code": code}).
		Decode(&cert)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("getting certificate by code: %w", err)
	}
	return &cert, nil
}

// RenderCertificateHTML renders the certificate as an HTML document suitable
// for PDF conversion. The template is intentionally self-contained (inline CSS,
// no external assets) so it works in headless renderers.
func RenderCertificateHTML(cert *Certificate, skillName string) (string, error) {
	tmpl, err := template.New("certificate").Parse(certificateHTMLTemplate)
	if err != nil {
		return "", fmt.Errorf("parsing certificate template: %w", err)
	}

	verificationURL := fmt.Sprintf("%s/certificates/%s", configs.APIPublicURL(), cert.VerificationCode)

	data := map[string]string{
		"ArtisanName":      cert.ArtisanName,
		"FundisID":         cert.FundisID,
		"Skill":            skillName,
		"Grade":            fmt.Sprintf("%d%%", cert.Grade),
		"GradeDescriptor":  cert.GradeDescriptor,
		"IssuedAt":         cert.IssuedAt.Format("02 January 2006"),
		"ExpiresAt":        cert.ExpiresAt.Format("02 January 2006"),
		"VerificationCode": cert.VerificationCode,
		"VerificationURL":  verificationURL,
	}

	var buf strings.Builder
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("rendering certificate template: %w", err)
	}
	return buf.String(), nil
}

// certificateHTMLTemplate is the self-contained HTML template for PDF rendering.
const certificateHTMLTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<title>Fundis Certificate</title>
<style>
  body {
    font-family: 'Georgia', serif;
    margin: 0;
    padding: 40px;
    background: #fff;
    color: #333;
  }
  .certificate {
    border: 3px solid #1a5276;
    padding: 60px 50px;
    max-width: 800px;
    margin: 0 auto;
    text-align: center;
  }
  .logo { font-size: 28px; font-weight: bold; color: #1a5276; margin-bottom: 10px; }
  .title { font-size: 22px; margin: 20px 0 10px; color: #1a5276; }
  .subtitle { font-size: 14px; color: #666; margin-bottom: 30px; }
  .artisan-name { font-size: 32px; font-weight: bold; margin: 20px 0; color: #1a5276; }
  .skill { font-size: 20px; margin: 10px 0; }
  .grade { font-size: 18px; margin: 10px 0; font-weight: bold; }
  .details { font-size: 13px; color: #555; margin-top: 30px; }
  .details p { margin: 5px 0; }
  .verification {
    margin-top: 30px;
    padding-top: 20px;
    border-top: 1px solid #ddd;
    font-size: 12px;
    color: #777;
  }
</style>
</head>
<body>
<div class="certificate">
  <div class="logo">FUNDIS</div>
  <div class="title">Certificate of Professional Competence</div>
  <div class="subtitle">This certifies that</div>
  <div class="artisan-name">{{.ArtisanName}}</div>
  <div class="skill">has demonstrated professional competence in <strong>{{.Skill}}</strong></div>
  <div class="grade">Grade: {{.Grade}} ({{.GradeDescriptor}})</div>
  <div class="details">
    <p>Fundis ID: {{.FundisID}}</p>
    <p>Issued: {{.IssuedAt}}</p>
    <p>Valid until: {{.ExpiresAt}}</p>
  </div>
  <div class="verification">
    <p>Verification Code: {{.VerificationCode}}</p>
    <p>Verify at: {{.VerificationURL}}</p>
  </div>
</div>
</body>
</html>`
