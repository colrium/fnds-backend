package profiles

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strings"

	"backend/configs"
	"backend/reviews"
	"backend/users"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// artisanShareData is the view model for the server-rendered artisan share page.
type artisanShareData struct {
	Name         string
	Skill        string
	City         string
	Experience   string
	Rating       string
	ReviewCount  int
	MetaDesc     string
	CanonicalURL string
	ProfileURL   string
	Monogram     string
	IsAvailable  bool
	IsVerified   bool
	JSONLD       template.JS
}

var artisanSharePage = template.Must(template.New("artisan").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="UTF-8" />
<meta name="viewport" content="width=device-width, initial-scale=1.0" />
<title>{{.Name}} - {{.Skill}} in {{.City}} | Fundis</title>
<meta name="description" content="{{.MetaDesc}}" />
<link rel="canonical" href="{{.CanonicalURL}}" />
<meta property="og:type" content="profile" />
<meta property="og:site_name" content="Fundis" />
<meta property="og:title" content="{{.Name}} - {{.Skill}} in {{.City}}" />
<meta property="og:description" content="{{.MetaDesc}}" />
<meta property="og:url" content="{{.CanonicalURL}}" />
<meta name="twitter:card" content="summary" />
<meta name="twitter:title" content="{{.Name}} - {{.Skill}} in {{.City}}" />
<meta name="twitter:description" content="{{.MetaDesc}}" />
<script type="application/ld+json">{{.JSONLD}}</script>
<style>
  :root { --primary: #FF5318; }
  * { box-sizing: border-box; }
  body { margin: 0; font-family: system-ui, -apple-system, "Segoe UI", Roboto, sans-serif; background: #f9fafb; color: #111827; }
  header { background: #fff; border-bottom: 1px solid #e5e7eb; }
  .bar { max-width: 768px; margin: 0 auto; padding: 16px; font-weight: 700; color: var(--primary); }
  main { max-width: 768px; margin: 0 auto; padding: 32px 16px; }
  .card { background: #fff; border: 1px solid #e5e7eb; border-radius: 12px; padding: 24px; box-shadow: 0 1px 2px rgba(0,0,0,.05); }
  .head { display: flex; align-items: center; gap: 16px; border-bottom: 1px solid #e5e7eb; padding-bottom: 16px; flex-wrap: wrap; }
  .mono { width: 48px; height: 48px; border-radius: 50%; background: #1f2937; color: #fff; font-weight: 700; display: flex; align-items: center; justify-content: center; font-size: 18px; }
  h1 { font-size: 20px; margin: 0; }
  .skill { color: #6b7280; font-size: 14px; margin: 2px 0 0; }
  .badges { display: flex; gap: 8px; margin-top: 8px; }
  .badge { font-size: 12px; padding: 2px 8px; border-radius: 9999px; font-weight: 500; }
  .badge-green { background: #d1fae5; color: #065f46; }
  .badge-blue { background: #dbeafe; color: #1e40af; }
  .grid { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; margin-top: 16px; font-size: 14px; }
  .grid .label { color: #6b7280; font-weight: 500; }
  .cta { margin-top: 20px; text-align: center; }
  .cta a { display: inline-block; background: var(--primary); color: #fff; text-decoration: none; font-weight: 600; font-size: 14px; padding: 10px 24px; border-radius: 8px; }
  @media (max-width: 640px) { .grid { grid-template-columns: 1fr; } }
</style>
</head>
<body>
<header><div class="bar">Fundis</div></header>
<main>
  <div class="card">
    <div class="head">
      <div class="mono">{{.Monogram}}</div>
      <div>
        <h1>{{.Name}}</h1>
        <p class="skill">{{.Skill}} in {{.City}}</p>
        <div class="badges">
          {{if .IsAvailable}}<span class="badge badge-green">Available</span>{{end}}
          {{if .IsVerified}}<span class="badge badge-blue">Verified</span>{{end}}
        </div>
      </div>
    </div>
    <div class="grid">
      <div><span class="label">Experience:</span> {{.Experience}}</div>
      <div><span class="label">Rating:</span> {{.Rating}} ({{.ReviewCount}} reviews)</div>
      <div><span class="label">Location:</span> {{.City}}</div>
    </div>
    <div class="cta">
      <a href="{{.ProfileURL}}">View Full Profile on Fundis</a>
    </div>
  </div>
</main>
</body>
</html>`))

type artisanShareDependencies struct {
	getUserBySlug           func(context.Context, string) (*users.User, error)
	getPublicUserByFundisID func(context.Context, string) (*users.User, error)
	getProfileByUserID      func(context.Context, string) (*Profile, error)
	listReviews             func(context.Context, bson.M) ([]reviews.Review, error)
}

func defaultArtisanShareDependencies() artisanShareDependencies {
	return artisanShareDependencies{
		getUserBySlug:           users.GetUserBySlug,
		getPublicUserByFundisID: users.GetPublicUserByFundisID,
		getProfileByUserID:      GetProfileByUserID,
		listReviews:             reviews.ListReviewsContext,
	}
}

// HandleArtisanSharePage server-renders a crawlable HTML page for an artisan
// so shared links unfurl and index without executing JavaScript.
func HandleArtisanSharePage(ctx echo.Context) error {
	return handleArtisanSharePage(ctx, defaultArtisanShareDependencies())
}

func handleArtisanSharePage(ctx echo.Context, deps artisanShareDependencies) error {
	slug := ctx.Param("slug")
	if !slugPattern.MatchString(slug) {
		// Might be a fundis_id — try to resolve and redirect to the slug URL.
		if slug != "" {
			user, err := deps.getPublicUserByFundisID(ctx.Request().Context(), slug)
			if err == nil && user.Slug != "" {
				return ctx.Redirect(http.StatusFound,
					"/artisan/"+user.Slug)
			}
		}
		return ctx.HTML(http.StatusBadRequest, "<!doctype html><meta charset=utf-8><title>Invalid artisan link | Fundis</title><p>The artisan profile link is invalid.</p>")
	}
	user, err := deps.getUserBySlug(ctx.Request().Context(), slug)
	if err != nil {
		return ctx.HTML(http.StatusNotFound, "<!doctype html><meta charset=utf-8><title>Artisan not found | Fundis</title><p>This artisan profile could not be found.</p>")
	}

	profile, err := deps.getProfileByUserID(ctx.Request().Context(), user.ID)
	if err != nil {
		if !errors.Is(err, mongo.ErrNoDocuments) {
			slog.ErrorContext(
				ctx.Request().Context(),
				"failed to fetch profile for artisan share page",
				"user_id",
				user.ID,
				"slug",
				slug,
				"error",
				err,
			)
			return ctx.HTML(http.StatusInternalServerError, "<!doctype html><meta charset=utf-8><title>Fundis</title><p>Something went wrong.</p>")
		}
		// No profile document yet — render the share page with user data only.
		slog.InfoContext(
			ctx.Request().Context(),
			"profile missing for artisan share page, rendering with user data only",
			"user_id",
			user.ID,
			"slug",
			slug,
		)
		profile = nil
	}

	name := strings.TrimSpace(user.FirstName + " " + user.LastName)
	skill := "Professional"
	city := "Kenya"
	if profile != nil {
		if len(profile.Skills) > 0 && profile.Skills[0] != "" {
			skill = profile.Skills[0]
		}
		if profile.City != "" {
			city = profile.City
		}
	}

	// Compute rating
	artisanReviews, err := deps.listReviews(ctx.Request().Context(), bson.M{"artisan_id": user.ID})
	if err != nil {
		slog.ErrorContext(
			ctx.Request().Context(),
			"failed to fetch reviews for artisan share page",
			"user_id",
			user.ID,
			"slug",
			slug,
			"error",
			err,
		)
		return ctx.HTML(http.StatusInternalServerError, "<!doctype html><meta charset=utf-8><title>Fundis</title><p>Something went wrong.</p>")
	}
	reviewCount := len(artisanReviews)
	var avgRating float64
	if reviewCount > 0 {
		var total int
		for _, r := range artisanReviews {
			total += r.Rating
		}
		avgRating = float64(total) / float64(reviewCount)
	}

	experience := "N/A"
	if profile != nil && profile.YearsOfExperience > 0 {
		experience = fmt.Sprintf("%d years", profile.YearsOfExperience)
	}

	ratingStr := fmt.Sprintf("%.1f", avgRating)
	if avgRating == 0 {
		ratingStr = "No ratings yet"
	}

	metaDesc := fmt.Sprintf("%s is a %s in %s with %s of experience. Hire on Fundis.", name, skill, city, experience)
	if len(metaDesc) > 160 {
		metaDesc = metaDesc[:160]
	}

	canonicalURL := strings.TrimRight(configs.APIPublicURL(), "/") + "/artisan/" + slug
	profileURL := strings.TrimRight(configs.AppPublicURL(), "/") + "/artisans/" + slug

	monogram := "FU"
	if len(user.FirstName) > 0 && len(user.LastName) > 0 {
		monogram = strings.ToUpper(string(user.FirstName[0]) + string(user.LastName[0]))
	}

	jsonLD := buildArtisanJSONLD(name, skill, city, avgRating, reviewCount, canonicalURL)

	data := artisanShareData{
		Name:         name,
		Skill:        skill,
		City:         city,
		Experience:   experience,
		Rating:       ratingStr,
		ReviewCount:  reviewCount,
		MetaDesc:     metaDesc,
		CanonicalURL: canonicalURL,
		ProfileURL:   profileURL,
		Monogram:     monogram,
		IsAvailable:  user.IsAvailable,
		IsVerified:   user.IsVerified,
		JSONLD:       template.JS(jsonLD),
	}

	var buf bytes.Buffer
	if err := artisanSharePage.Execute(&buf, data); err != nil {
		return ctx.HTML(http.StatusInternalServerError, "<!doctype html><meta charset=utf-8><title>Fundis</title><p>Something went wrong.</p>")
	}
	ctx.Response().Header().Set("Cache-Control", "public, s-maxage=300, max-age=60")
	return ctx.HTML(http.StatusOK, buf.String())
}

func buildArtisanJSONLD(name, skill, city string, rating float64, reviewCount int, canonicalURL string) string {
	ld := map[string]any{
		"@context": "https://schema.org/",
		"@type":    "ProfessionalService",
		"name":     name + " - " + skill,
		"url":      canonicalURL,
		"areaServed": map[string]any{
			"@type": "Place",
			"name":  city,
		},
		"address": map[string]any{
			"@type":           "PostalAddress",
			"addressLocality": city,
			"addressCountry":  "KE",
		},
	}
	if reviewCount > 0 {
		ld["aggregateRating"] = map[string]any{
			"@type":       "AggregateRating",
			"ratingValue": fmt.Sprintf("%.1f", rating),
			"reviewCount": reviewCount,
			"bestRating":  5,
			"worstRating": 1,
		}
	}
	b, err := json.Marshal(ld)
	if err != nil {
		return "{}"
	}
	return string(b)
}
