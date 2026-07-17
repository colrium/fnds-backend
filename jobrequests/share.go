package jobrequests

import (
	"bytes"
	"encoding/json"
	"html"
	"html/template"
	"net/http"
	"strings"
	"time"

	"backend/configs"

	"github.com/labstack/echo/v4"
)

// vacancyShareData is the view model for the server-rendered share page.
type vacancyShareData struct {
	Title       string
	Company     string
	Description string
	MetaDesc    string
	CanonicalURL string
	ApplyURL    string
	Monogram    string
	StatusOpen  bool
	Fields      []shareField
	JSONLD      template.JS
}

type shareField struct {
	Label string
	Value string
}

var vacancySharePage = template.Must(template.New("vacancy").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="UTF-8" />
<meta name="viewport" content="width=device-width, initial-scale=1.0" />
<title>{{.Title}} at {{.Company}} | Fundis</title>
<meta name="description" content="{{.MetaDesc}}" />
<link rel="canonical" href="{{.CanonicalURL}}" />
<meta property="og:type" content="website" />
<meta property="og:site_name" content="Fundis" />
<meta property="og:title" content="{{.Title}} at {{.Company}}" />
<meta property="og:description" content="{{.MetaDesc}}" />
<meta property="og:url" content="{{.CanonicalURL}}" />
<meta name="twitter:card" content="summary" />
<meta name="twitter:title" content="{{.Title}} at {{.Company}}" />
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
  .head { display: flex; align-items: center; justify-content: space-between; gap: 16px; border-bottom: 1px solid #e5e7eb; padding-bottom: 16px; flex-wrap: wrap; }
  .id { display: flex; align-items: center; gap: 12px; }
  .mono { width: 48px; height: 48px; border-radius: 6px; background: #1f2937; color: #fff; font-weight: 700; display: flex; align-items: center; justify-content: center; }
  h1 { font-size: 20px; margin: 0; }
  .company { color: #6b7280; font-size: 14px; margin: 2px 0 0; }
  .apply { display: inline-block; background: var(--primary); color: #fff; text-decoration: none; font-weight: 600; font-size: 14px; padding: 10px 20px; border-radius: 8px; }
  .grid { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; margin-top: 16px; font-size: 14px; }
  .grid .label { color: #6b7280; font-weight: 500; }
  .section { margin-top: 16px; border-top: 1px solid #e5e7eb; padding-top: 16px; font-size: 14px; }
  .section p { color: #4b5563; white-space: pre-line; margin: 4px 0 0; }
  .closed { margin-top: 16px; background: #fffbeb; color: #b45309; padding: 8px 16px; border-radius: 6px; font-size: 14px; }
  @media (max-width: 640px) { .grid { grid-template-columns: 1fr; } }
</style>
</head>
<body>
<header><div class="bar">Fundis</div></header>
<main>
  <div class="card">
    <div class="head">
      <div class="id">
        <div class="mono">{{.Monogram}}</div>
        <div>
          <h1>{{.Title}}</h1>
          <p class="company">{{.Company}}</p>
        </div>
      </div>
      {{if .StatusOpen}}<a class="apply" href="{{.ApplyURL}}">Apply Now</a>{{end}}
    </div>
    {{if not .StatusOpen}}<div class="closed">This vacancy is no longer accepting applications.</div>{{end}}
    <div class="grid">
      {{range .Fields}}<div><span class="label">{{.Label}}:</span> {{.Value}}</div>{{end}}
    </div>
    <div class="section">
      <span class="label">Description</span>
      <p>{{.Description}}</p>
    </div>
    <div class="section">
      <a class="apply" href="{{.ApplyURL}}">{{if .StatusOpen}}Apply Now{{else}}View on Fundis{{end}}</a>
    </div>
  </div>
</main>
</body>
</html>`))

func fmtShareDate(value string) string {
	if value == "" {
		return "N/A"
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return value
	}
	return t.Format("Jan 2, 2006")
}

func orNA(value string) string {
	if strings.TrimSpace(value) == "" {
		return "N/A"
	}
	return value
}

func schemaEmploymentType(value string) string {
	switch value {
	case "FULL_TIME":
		return "FULL_TIME"
	case "PART_TIME":
		return "PART_TIME"
	case "CONTRACT":
		return "CONTRACTOR"
	case "INTERNSHIP":
		return "INTERN"
	default:
		return ""
	}
}

// HandleVacancySharePage server-renders a crawlable HTML page for a vacancy so
// shared links unfurl and index without executing JavaScript. Humans clicking
// "Apply" are deep-linked into the SPA.
func HandleVacancySharePage(ctx echo.Context) error {
	id := ctx.Param("id")
	v, err := GetJobVacancyByID(id)
	if err != nil {
		return ctx.HTML(http.StatusNotFound, "<!doctype html><meta charset=utf-8><title>Vacancy not found | Fundis</title><p>This vacancy could not be found.</p>")
	}

	company := orNA(v.CompanyName)
	metaDesc := strings.Join(strings.Fields(v.Description), " ")
	if len(metaDesc) > 160 {
		metaDesc = metaDesc[:160]
	}
	if metaDesc == "" {
		metaDesc = v.Title + " vacancy at " + company + "."
	}

	appURL := strings.TrimRight(configs.AppPublicURL(), "/")
	applyURL := appURL + "/vacancy/" + v.ID
	canonicalURL := strings.TrimRight(configs.APIPublicURL(), "/") + "/vacancy/" + v.ID

	monogram := "CO"
	if len(v.CompanyName) >= 2 {
		monogram = strings.ToUpper(v.CompanyName[:2])
	}

	jsonLD := buildJobPostingJSONLD(v, company)

	data := vacancyShareData{
		Title:        orNA(v.Title),
		Company:      company,
		Description:  orNA(v.Description),
		MetaDesc:     metaDesc,
		CanonicalURL: canonicalURL,
		ApplyURL:     applyURL,
		Monogram:     monogram,
		StatusOpen:   v.Status == "" || v.Status == "open",
		Fields: []shareField{
			{"Skill", orNA(v.Skill)},
			{"Level", orNA(v.Level)},
			{"Type", orNA(v.EmploymentType)},
			{"Location", orNA(v.Location)},
			{"Experience", orNA(v.Experience)},
			{"Salary", orNA(v.SalaryRange)},
			{"Closes", fmtShareDate(v.ApplicationEndDate)},
		},
		JSONLD: template.JS(jsonLD),
	}

	var buf bytes.Buffer
	if err := vacancySharePage.Execute(&buf, data); err != nil {
		return ctx.HTML(http.StatusInternalServerError, "<!doctype html><meta charset=utf-8><title>Fundis</title><p>Something went wrong.</p>")
	}
	return ctx.HTML(http.StatusOK, buf.String())
}

func buildJobPostingJSONLD(v *JobVacancy, company string) string {
	posting := map[string]any{
		"@context":    "https://schema.org/",
		"@type":       "JobPosting",
		"title":       v.Title,
		"description": html.EscapeString(orNA(v.Description)),
		"hiringOrganization": map[string]any{
			"@type": "Organization",
			"name":  company,
		},
		"jobLocation": map[string]any{
			"@type": "Place",
			"address": map[string]any{
				"@type":           "PostalAddress",
				"addressLocality": firstNonEmpty(v.Location, v.CompanyLocation, "Kenya"),
				"addressCountry":  "KE",
			},
		},
	}
	if !v.CreatedAt.IsZero() {
		posting["datePosted"] = v.CreatedAt.Format(time.RFC3339)
	}
	if et := schemaEmploymentType(v.EmploymentType); et != "" {
		posting["employmentType"] = et
	}
	if v.ApplicationEndDate != "" {
		posting["validThrough"] = v.ApplicationEndDate
	}
	if v.CompanyWebsite != "" {
		if org, ok := posting["hiringOrganization"].(map[string]any); ok {
			org["sameAs"] = v.CompanyWebsite
		}
	}
	b, err := json.Marshal(posting)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
